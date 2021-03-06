package main

import (
	"bytes"
	"compress/gzip"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"time"

	log "github.com/inconshreveable/log15"
	yaml "gopkg.in/yaml.v2"
)

func (C *Config) getConf(configPath string) *Config {

	pwd, _ := os.Getwd()
	yamlFile, err := ioutil.ReadFile(path.Join(pwd, configPath))
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
	err = yaml.Unmarshal(yamlFile, C)
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}

	return C
}

// Prox defines our reverse proxy
type Prox struct {
	config *Config
	target *url.URL
	proxy  *httputil.ReverseProxy
	log    log.Logger
}

// Trace - Request error handling wrapper on the handler
type Trace struct {
	Path    string
	Method  string
	Error   string
	Message string
	Code    int
	Elapsed int
	User    string
	Groups  []string
	Body    string
	Access  []string
}

// NewProx returns new reverse proxy instance
func NewProx(C *Config) *Prox {
	url, _ := url.Parse(C.Target)

	logger := log.New()
	if C.JSONlogging {
		logger.SetHandler(log.MultiHandler(log.StreamHandler(os.Stderr,
			log.JsonFormat())))
	}

	return &Prox{
		config: C,
		target: url,
		proxy:  httputil.NewSingleHostReverseProxy(url),
		log:    logger,
	}
}

type traceTransport struct {
	Response *http.Response
}

func (p *Prox) handleRequest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	trace := Trace{}

	ctx, err := getRequestContext(r, p.config, &trace)
	if err != nil {
		trace.Error = err.Error()
		w.WriteHeader(http.StatusBadRequest)
	}

	trans := traceTransport{}
	p.proxy.Transport = &trans

	ok, err := p.checkRBAC(ctx)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
	} else if err != nil {
		trace.Error = err.Error()
		w.WriteHeader(http.StatusUnauthorized)
	} else {
		p.proxy.ServeHTTP(w, r)
	}

	trace.Elapsed = int(time.Since(start) / time.Millisecond)
	if trans.Response != nil {
		trace.Code = trans.Response.StatusCode
	} else {
		trace.Code = 403
	}

	trace.Method = r.Method

	fields := log.Ctx{
		"code":    trace.Code,
		"method":  r.Method,
		"path":    r.URL.Path,
		"elasped": trace.Elapsed,
		"user":    trace.User,
		"groups":  trace.Groups,
		"body":    trace.Body,
		"access":  trace.Access,
	}

	if err != nil {
		p.log.Error(trace.Error, fields)
	} else if trace.Code != 200 {
		p.log.Warn(trace.Message, fields)
	} else {
		p.log.Info(trace.Message, fields)
	}
}

func (t *traceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		return res, err
	}

	if res.Header.Get("Content-Encoding") == "gzip" {
		body, err := gzip.NewReader(res.Body)
		if err != nil {
			return res, err
		}
		res.Body = body
		res.Header.Del("Content-Encoding")
		res.Header.Del("Content-Length")
		res.ContentLength = -1
		res.Uncompressed = true
	}

	t.Response = res

	return res, nil
}

func getBody(r *http.Request) ([]byte, error) {
	var body []byte
	buf, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return body, err
	}
	rdr1 := ioutil.NopCloser(bytes.NewBuffer(buf))
	body, err = ioutil.ReadAll(rdr1)
	if err != nil {
		return body, err
	}
	// If we don't keep a second reader untouched, we will consume
	// the request body when reading it
	rdr2 := ioutil.NopCloser(bytes.NewBuffer(buf))
	// restore the body from the second reader
	r.Body = rdr2

	return body, nil
}
