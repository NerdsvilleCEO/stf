/* -.-.-.-.-.-.-.-.-.-.-.-.-.-.-.-.-.-.-.-.

* File Name : main.go

* Purpose :

* Creation Date : 12-14-2015

* Last Modified : Sun 15 Apr 2018 03:44:36 PM UTC

* Created By : Kiyor

_._._._._._._._._._._._._._._._._._._._._.*/

package main

import (
	"bytes"
	"context"
	"math"
	// 	"crypto/tls"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/dustin/go-humanize"
	"github.com/juju/ratelimit"
	"github.com/kiyor/go-socks5"
	"github.com/viki-org/dnscache"
	"github.com/wsxiaoys/terminal/color"
	"golang.org/x/net/proxy"
)

var (
	fdir     *string = flag.String("d", ".", "Mount Dir")
	fport    *string = flag.String("p", ":30000", "Listening Port")
	upstream *string = flag.String("upstream", "scheme://ip:port or ip:port", "setup proxy")
	next     *string = flag.String("next", "[ip:port](:user:pass)", "proxy mode with socks5 proxy to upstream")

	httpAuthFlag *string = flag.String("httpauth", "", "http base auth mode, import txt/json/string")

	sock              *bool   = flag.Bool("socks5", false, "socks5 mode")
	sockAuth          *string = flag.String("socks5auth", "", "socks5 auth mode, import txt/json/string")
	sockHosts         *string = flag.String("socks5hosts", "", "socks5 hosts file")
	sockNext          *string = flag.String("socks5next", "", "socks5 proxy chan next point")
	sockNoResolver    *bool   = flag.Bool("socks5noresolver", false, "socks5 without resolver for proxy chan")
	socksInRateLimit          = flag.Int64("socks5in", math.MaxInt64, "socks5 max input ratelimit (byte upload)")
	socksOutRateLimit         = flag.Int64("socks5out", math.MaxInt64, "socks5 max input ratelimit (byte download)")

	httpTunnel *bool = flag.Bool("tunnel", false, "http tunnel mode")
	uploadonly *bool = flag.Bool("uploadonly", false, "upload only POST/PUT")
	showBody   *bool = flag.Bool("body", false, "show body")

	testFile       *bool   = flag.Bool("testfile", false, "testfile, /1(K/M/G)")
	testFileCC     *string = flag.String("testcc", "public,max-age=60", "testfile Cache-Control Header")
	testDisCounter *bool   = flag.Bool("testc", false, "disable test counter")

	bridge               *string = flag.String("bridge", "host/ip/host:ip", "quick setup http/+https proxy with upstream 80/+443")
	crt                  *string = flag.String("crt", "", "crt location if using brdige mode")
	key                  *string = flag.String("key", "", "key location if using brdige mode")
	bridgeIp, bridgeHost string

	version *bool = flag.Bool("version", false, "output version and exit")

	rt      = flag.Int("return", -1, "debug test return code")
	threexx = flag.String("3xx", "", "301/302 https://{{.Host}}{{.URL}}")

	tcp      bool
	isbridge bool

	timeout   *time.Duration = flag.Duration("timeout", 5*time.Minute, "timeout")
	notimeout                = flag.Bool("notimeout", false, "no timeout")

	disableGzip = flag.Bool("gzip-disable", false, "disable gzip, default gzip = on")

	gzipTypes = flag.String("gzip-types", "text/html text/plain text/css text/javascript text/xml application/json application/javascript application/x-javascript application/xml application/atom+xml application/rss+xml application/vnd.ms-fontobject application/x-font-ttf font/opentype font/x-woff", "gzip type")

	proxyMethod = false

	reTestFile = regexp.MustCompile(`(\d+)(b|B|k|K|m|M|g|G)(.*)`)

	ch        = make(chan bool)
	wg        = new(sync.WaitGroup)
	stop      bool
	buildtime string
	VER       = "1.0"
	serveByte uint64
)

func init() {
	flag.Var(&flagAllowIP, "allow", "allow IP, -allow '1.1.1.1' -allow '2.2.2.0/24'")
	flag.Var(&flagDenyIP, "deny", "deny IP, -deny '1.1.1.1' -deny '2.2.2.0/24'")
	flag.Parse()
	if *version {
		fmt.Printf("%v.%v", VER, buildtime)
		os.Exit(0)
	}
	if *upstream != "scheme://ip:port or ip:port" {
		proxyMethod = true
		u := *upstream
		if u[:4] != "http" {
			tcp = true
		}
	}
	if *bridge != "host/ip/host:ip" {
		isbridge = true
		proxyMethod = true
		p := strings.Split(*bridge, ":")
		if len(p) > 1 {
			bridgeHost = p[0]
			bridgeIp = p[1]
		} else {
			if ip := net.ParseIP(*bridge); ip == nil {
				bridgeHost = *bridge
			}
			bridgeIp = *bridge
		}
		*upstream = *bridge
	}
	p := *fport
	if p[:1] != ":" && !strings.Contains(*fport, ":") {
		p = ":" + p
		fport = &p
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

}

func getips() string {
	p := *fport
	if p[:1] != ":" && strings.Contains(*fport, ":") {
		return *fport
	}
	ips, err := net.InterfaceAddrs()
	if err != nil {
		panic(err)
	}
	var s string
	for _, v := range ips {
		ip := strings.Split(v.String(), "/")[0]
		if ip != "127.0.0.1" {
			s += strings.Split(v.String(), "/")[0] + *fport + " "
		}
	}
	return s
}

func byteCounter() {
	ticker := time.Tick(time.Second)
	var total uint64
	var max uint64
	var avg uint64
	var emptySecond float64
	t1 := time.Now()
	defer fmt.Println()
	for {
		<-ticker
		total += serveByte
		if serveByte == 0 {
			emptySecond += 1.0
		} else if serveByte > max {
			max = serveByte
		}
		if uint64(time.Since(t1).Seconds()-emptySecond) > 0 {
			avg = total / uint64(time.Since(t1).Seconds()-emptySecond)
		}
		fmt.Printf("\rspeed: %10v/s  total: %10v  max: %10v/s  avg: %10v/s", humanize.Bytes(serveByte), humanize.Bytes(total), humanize.Bytes(max), humanize.Bytes(avg))
		serveByte = 0
	}
}

func main() {

	runtime.GOMAXPROCS(runtime.NumCPU())

	if *testFile && !*testDisCounter {
		go byteCounter()
	}

	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if stop {
			return
		}
		wg.Add(1)
		if *veryverbose {
			dumpRequest(req, true, true)
		}
		defer wg.Done()
		ch <- true
		if proxyMethod {
			if isbridge {
				scheme := "https"
				if req.TLS == nil {
					scheme = "http"
				}
				u, _ := url.Parse(fmt.Sprintf("%s://%s", scheme, bridgeIp))
				httputil.NewSingleHostReverseProxy(u).ServeHTTP(w, req)
				// 				proxyHandler(w, req, fmt.Sprintf("%s://%s", scheme, bridgeIp))
			} else {
				u, _ := url.Parse(*upstream)
				httputil.NewSingleHostReverseProxy(u).ServeHTTP(w, req)
				// 				proxyHandler(w, req, *upstream)
			}
			return
		}
		// if not just return code
		if *rt != -1 {
			w.WriteHeader(*rt)
			dumpRequest(req, true, true)
			return
		}
		if len(*threexx) != 0 {
			code := (*threexx)[:3]
			c := 302
			var u string
			var err error
			c, err = strconv.Atoi(code)
			if err != nil {
				u = *threexx
				c = 302
			} else {
				u = (*threexx)[4:]
			}
			var buf bytes.Buffer
			t, _ := template.New("url").Parse(u)
			err = t.Execute(&buf, req)
			if err != nil {
				log.Println(err.Error())
			}
			http.Redirect(w, req, buf.String(), c)
			return
		}

		w.Header().Add("Connection", "Keep-Alive")
		if (req.Method == "GET" || req.Method == "HEAD") && !*uploadonly && !*testFile {
			if len(req.Header.Get("X-Cache-Control")) > 0 {
				w.Header().Add("Cache-Control", req.Header.Get("X-Cache-Control"))
			} else {
				w.Header().Add("Cache-Control", "no-cache")
			}
			f := &fileHandler{Dir(*fdir)}
			f.ServeHTTP(w, req)
		} else if *testFile {
			testFileHandler(w, req)
		} else if req.Method == "POST" || req.Method == "PUT" {
			uploadHandler(w, req)
		}
	})

	var h http.Handler

	if len(*httpAuthFlag) > 0 {
		h = httpAuth(handler)
	} else {
		h = handler
	}
	// 	if !*disableGzip {
	typ := strings.Split(*gzipTypes, " ")
	for _, v := range typ {
		typ = append(typ, v+"; charset=utf-8")
	}
	wrap, err := gziphandler.GzipHandlerWithOpts(gziphandler.MinSize(512), gziphandler.CompressionLevel(gzip.DefaultCompression), gziphandler.ContentTypes(typ))
	if err != nil {
		panic(err)
	}
	h = wrap(h)
	mux.Handle("/", LogHandler(h))

	log.Println("Listening on", getips())
	if proxyMethod {
		log.Println("Upstream", *upstream, "tcp", tcp)
	}
	if *testFile && !*testDisCounter {
		log.SetOutput(ioutil.Discard)
	}

	done := make(chan bool)

	if *sock {
		go func() {
			conf := &socks5.Config{}
			conf.Resolver = &Resolver{dnscache.New(time.Minute * 5)}
			conf.Rewriter = new(Rewriter)
			conf.Rules = new(FireWallRuleSet)
			if *socksInRateLimit != math.MaxInt64 {
				conf.InBucket = ratelimit.NewBucketWithRate(float64(*socksInRateLimit), *socksInRateLimit)
			}
			if *socksOutRateLimit != math.MaxInt64 {
				conf.OutBucket = ratelimit.NewBucketWithRate(float64(*socksOutRateLimit), *socksOutRateLimit)
			}
			if *sockNext != "" {
				var a *proxy.Auth
				p := strings.Split(*sockNext, ":")
				if len(p) > 2 {
					a = new(proxy.Auth)
					a.User = p[2]
					a.Password = p[3]
					*sockNext = strings.Join(p[:2], ":")
				}
				dialer, err := proxy.SOCKS5("tcp", *sockNext,
					a,
					&net.Dialer{
						KeepAlive: 30 * time.Second,
					},
				)
				if err != nil {
					log.Println(err.Error())
					os.Exit(1)
				}
				conf.Dial = func(ctx context.Context, net_, addr string) (net.Conn, error) {
					return dialer.Dial(net_, addr)
				}
			}
			conf.Logger = log.New(ioutil.Discard, "", log.LstdFlags)
			// 			conf.Logger = log.New(os.Stdout, "[socks5] ", log.LstdFlags)
			conf.Finalizer = &LogFinalizer{log.New(os.Stdout, "[socks5] ", log.LstdFlags)}
			if *sockAuth != "" {
				cred := parseSocks5Auth(*sockAuth)
				cator := socks5.UserPassAuthenticator{Credentials: cred}
				conf.AuthMethods = []socks5.Authenticator{cator}
			}
			if *sockHosts != "" {
				readHosts(*sockHosts)
				go watcher(*sockHosts, func(string) error { return readHosts(*sockHosts) })
			}
			server, err := socks5.New(conf)
			if err != nil {
				panic(err)
			}
			if *sockNoResolver {
				conf.Resolver = nil
			}

			if err := server.ListenAndServe("tcp", *fport); err != nil {
				panic(err)
			}
		}()
	} else if tcp {
		go tcpProxy()
	} else if *httpTunnel {
		go func() {
			if err := http.ListenAndServe(*fport, NewProxy()); err != nil {
				panic(err)
			}
		}()
	} else {
		if !isbridge {
			if len(*crt) > 0 && len(*key) > 0 {
				go func() {
					if err := http.ListenAndServeTLS(*fport, *crt, *key, mux); err != nil {
						panic(err)
					}
				}()
			} else {
				go func() {
					if err := http.ListenAndServe(*fport, mux); err != nil {
						panic(err)
					}
				}()
			}
		} else {
			go func() {
				if err := http.ListenAndServe(":80", mux); err != nil {
					panic(err)
				}
			}()
			if len(*crt) > 0 && len(*key) > 0 {
				go func() {
					if err := http.ListenAndServeTLS(":443", *crt, *key, mux); err != nil {
						panic(err)
					}
				}()
			}
		}
	}

	if *notimeout {
		*timeout = time.Duration(1<<63 - 1)
	}
	t := time.Tick(*timeout)
	go func() {
		for {
			select {
			case <-t:
				log.Println(os.Args[0], "auto stop, no more request accessable")
				stop = true
				wg.Wait()
				done <- true
			case <-ch:
				t = time.Tick(*timeout)
			}
		}
	}()

	if <-done {
		log.Println("stop")
		os.Exit(0)
	}
}

func Json(i interface{}) string {
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		log.Println(err.Error())
	}
	return string(b)
}

// dump request , body true/false, print true/false
func dumpRequest(r *http.Request, b, p bool) []byte {
	dump, err := httputil.DumpRequest(r, b)
	if err != nil {
		log.Println(err.Error())
	}
	if p {
		index := bytes.Index(dump, []byte("\r\n\r\n"))
		headers := dump[:index]
		body := bytes.TrimLeft(dump[index:], "\r\n\r\n")
		if *veryverbose {
			now := time.Now()
			host := "_"
			for _, v := range strings.Split(string(headers), "\n") {
				if len(v) > 5 && strings.ToUpper(v[:5]) == "HOST:" {
					host = strings.Split(v, " ")[1]
					host = strings.Trim(host, "\r")
				}
			}
			dirname := "/tmp/stfdump/" + host
			if _, err := os.Stat(dirname); err != nil {
				if err := os.MkdirAll(dirname, 0755); err != nil {
					log.Fatalln(err.Error())
				}
			}
			filename := fmt.Sprintf("%s/%d>", dirname, now.UnixNano())
			ioutil.WriteFile(filename, body, 0644)
		}
		if *colors {
			color.Printf("@{b}%v@{|}\n", string(headers))
			if *showBody {
				color.Printf("@{g}%v@{|}\n", string(body))
			}
		} else {
			fmt.Println(string(headers))
			fmt.Println(string(body))
		}
	}
	return dump
}

// dump request , body true/false, print true/false
func dumpResponse(r *http.Response, b, p bool, host string) []byte {
	dump, err := httputil.DumpResponse(r, b)
	if err != nil {
		log.Println(err.Error())
	}
	// 	isGzip := false
	// 	if v, ok := r.Header["Accept-Encoding"]; ok {
	// 		if strings.Contains(v[0], "gzip") {
	// 			isGzip = true
	// 			log.Println("is gzip")
	// 		}
	// 	}
	if p {
		index := bytes.Index(dump, []byte("\r\n\r\n"))
		headers := dump[:index]
		body := bytes.TrimLeft(dump[index:], "\r\n\r\n")
		// 		body = bytes.TrimLeft(body, string([]byte{13, 10, 13, 10}))

		// 		if isGzip {
		// 			reader := bytes.NewReader(body)
		// 			g, err := gzip.NewReader(reader)
		// 			if err != nil {
		// 				log.Println(err.Error())
		// 			}
		// 			body, err = ioutil.ReadAll(g)
		// 			if err != nil {
		// 				log.Println(err.Error())
		// 			}
		// 		}
		if *veryverbose {
			now := time.Now()
			dirname := "/tmp/stfdump/" + host
			if _, err := os.Stat(dirname); err != nil {
				if err := os.MkdirAll(dirname, 0755); err != nil {
					log.Fatalln(err.Error())
				}
			}
			filename := fmt.Sprintf("%s/%d<", dirname, now.UnixNano())
			ioutil.WriteFile(filename, body, 0644)
		}
		if *colors {
			// 			color.Printf("@{b}%s@{|}", string(dump))
			color.Printf("@{c}%v@{|}\n", string(headers))
			if *showBody {
				color.Printf("@{g}%v@{|}\n", string(body))
			}
			// 			color.Printf("@{g}%v@{|}\n", ehex.EncodeToString(body))
			// 			color.Printf("@{g}%v@{|}\n", body)
		} else {
			fmt.Println(string(headers))
			fmt.Println(string(body))
		}
	}
	return dump
}

/*
func proxyHandler(w http.ResponseWriter, r *http.Request, upper string) {
	var path string
	var host string
	if strings.Contains(r.URL.String(), "http") {
		path = r.URL.String()
		host = r.URL.Host
	} else {
		path = upper + r.URL.RequestURI()
		host = r.Host
	}
	req, err := http.NewRequest(r.Method, path, r.Body)
	if err != nil {
		panic(err)
	}
	if len(bridgeHost) > 0 {
		req.Host = bridgeHost
	}
	if ip := net.ParseIP(r.Host); ip == nil {
		req.Host = host
	}
	t1 := time.Now()

	for k, v := range r.Header {
		for i, vv := range v {
			if i == 0 {
				req.Header.Set(k, vv)
			} else {
				req.Header.Add(k, vv)
			}
		}
	}

	proxyClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Dial:            toDial(*next),
		},
	}

	resp, err := proxyClient.Do(req)
	if err != nil {
		fmt.Fprintf(w, err.Error())
		if resp != nil {
			w.WriteHeader(resp.StatusCode)
		} else {
			w.WriteHeader(500)
		}
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)

	for k, v := range resp.Header {
		for _, v1 := range v {
			w.Header().Set(k, v1)
		}
	}
	w.Header().Set("X-Upstream-Response-Time", NanoToSecond(time.Since(t1)))

	if *veryverbose {
		dumpResponse(resp, true, true, req.Host)
	}
	io.Copy(w, resp.Body)
}
*/

func NanoToSecond(d time.Duration) string {
	return fmt.Sprintf("%.3f", float64(d.Nanoseconds())/1000000)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	p := *fdir + "/" + r.URL.Path
	d, _ := filepath.Split(p)

	f, err := os.Open(d)
	defer f.Close()

	if err != nil {
		err = os.MkdirAll(d, 0755)
		if err != nil {
			fmt.Fprintf(w, "%s\n", err.Error())
			log.Println(err.Error())
		}
		f, _ = os.Open(d)
	}
	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintf(w, "%s\n", err.Error())
		log.Println(err.Error())
		return
	}
	if fi.Mode().IsRegular() {
		fmt.Fprintf(w, "%s is a file\n", d)
		log.Println(d, "is a file")
		return
	}

	out, err := os.Create(p)
	if err != nil {
		fmt.Fprintf(w, "Unable to create the file for writing '%v'. Check your write access privilege\n", p)
		return
	}

	defer out.Close()

	_, err = io.Copy(out, r.Body)
	if err != nil {
		fmt.Fprintln(w, err)
	}

	fmt.Fprintf(w, "File uploaded successfully : %s\n", p)
}
