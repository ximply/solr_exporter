package main

import (
        "flag"
        "net"
        "os"
        "net/http"
        "io"
        "github.com/parnurzeal/gorequest"
        "github.com/widuu/gojson"
        "github.com/robfig/cron"
        "time"
        "fmt"
        "strconv"
        "sync"
        "io/ioutil"
        "context"
        "strings"
)

var (
        Name           = "solr_exporter"
        listenAddress  = flag.String("unix-sock", "/dev/shm/solr_exporter.sock", "Address to listen on for unix sock access and telemetry.")
        metricsPath    = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
        solrUrl        = flag.String("solr-url", "http://localhost:8080/solr", "Destination solr url(http://localhost:8080/solr).")
        solrDetail     = flag.String("solr-detail", "/dev/shm/solr_detail_exporter.sock", "Solr detail metrics")
)

var g_doing bool
var g_ret string
var g_lock sync.RWMutex

type threads struct {
        Curr float64
        Peak float64
        Daemon float64
}

type heapMem struct {
        Free float64
        Total float64
        Max float64
        Used float64
}

func metrics(w http.ResponseWriter, r *http.Request) {
        g_lock.RLock()
        io.WriteString(w, g_ret)
        g_lock.RUnlock()
}

func threadsInfo() threads {
        // http://localhost:8080/solr/admin/info/threads?wt=json
        thds := threads {
                Curr:-1,
                Peak:-1,
                Daemon:-1,
        }
        req := gorequest.New()
        _, body, errs := req.Retry(1, 3 * time.Second,
                http.StatusBadRequest, http.StatusInternalServerError).Get(fmt.Sprintf("%s/admin/info/threads?wt=json", *solrUrl)).End()
        if errs != nil {
                return thds
        }
        curr, err := strconv.ParseFloat(gojson.Json(body).Get("system").Get("threadCount").Get("current").Tostring(), 64)
        if err != nil {
                curr = -1
        }
        peak, err := strconv.ParseFloat(gojson.Json(body).Get("system").Get("threadCount").Get("peak").Tostring(), 64)
        if err != nil {
                peak = -1
        }
        daemon, err := strconv.ParseFloat(gojson.Json(body).Get("system").Get("threadCount").Get("daemon").Tostring(), 64)
        if err != nil {
                daemon = -1
        }

        thds.Curr = curr
        thds.Peak = peak
        thds.Daemon = daemon
        return thds
}

func heapMemInfo() heapMem {
        hm := heapMem{
                Free:-1,
                Total:-1,
                Max:-1,
                Used:-1,
        }

        req := gorequest.New()
        _, body, errs := req.Retry(1, 3 * time.Second,
                http.StatusBadRequest, http.StatusInternalServerError).Get(fmt.Sprintf("%s/admin/info/system?wt=json", *solrUrl)).End()
        if errs != nil {
                return hm
        }
        free, err := strconv.ParseFloat(gojson.Json(body).Get("jvm").Get("memory").Get("raw").Get("free").Tostring(), 64)
        if err != nil {
                free = -1
        }
        total, err := strconv.ParseFloat(gojson.Json(body).Get("jvm").Get("memory").Get("raw").Get("total").Tostring(), 64)
        if err != nil {
                total = -1
        }
        max, err := strconv.ParseFloat(gojson.Json(body).Get("jvm").Get("memory").Get("raw").Get("max").Tostring(), 64)
        if err != nil {
                max = -1
        }
        used, err := strconv.ParseFloat(gojson.Json(body).Get("jvm").Get("memory").Get("raw").Get("used").Tostring(), 64)
        if err != nil {
                used = -1
        }

        hm.Free = free
        hm.Total = total
        hm.Max = max
        hm.Used = used
        return hm
}

type UnixResponse struct {
        Rsp string
        Status string
}

func metricsFromUnixSock(unixSockFile string, metricsPath string, timeout time.Duration) UnixResponse {
        rsp := UnixResponse{
                Rsp: "",
                Status: "500",
        }

        c := http.Client{
                Transport: &http.Transport{
                        DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
                                return net.Dial("unix", unixSockFile)
                        },
                        DisableKeepAlives: true,
                },
                Timeout: timeout,
        }
        res, err := c.Get(fmt.Sprintf("http://unix/%s", metricsPath))
        if res != nil {
                defer res.Body.Close()
        }
        if err != nil {
                return rsp
        }

        body, err := ioutil.ReadAll(res.Body)
        if err != nil {
                return rsp
        }
        rsp.Rsp = string(body)
        rsp.Status = "200"
        return rsp
}

func detailInfo() string {
        rsp := metricsFromUnixSock(*solrDetail,"metrics", 5 * time.Second)
        if strings.Compare(rsp.Status, "200") != 0 {
                return ""
        }
        return rsp.Rsp
}

func doWork() {
        if g_doing {
                return
        }
        g_doing = true

        ret := ""
        nameSpace := "solr"

        thds := threadsInfo()
        if thds.Curr > -1 {
                ret = ret + fmt.Sprintf("%s_threads{type=\"current\"} %g\n", nameSpace, thds.Curr)
                ret = ret + fmt.Sprintf("%s_threads{type=\"peak\"} %g\n", nameSpace, thds.Peak)
                ret = ret + fmt.Sprintf("%s_threads{type=\"daemon\"} %g\n", nameSpace, thds.Daemon)
        }
        hm := heapMemInfo()
        if hm.Used > -1 {
                ret = ret + fmt.Sprintf("%s_heap_memory{type=\"used\"} %g\n", nameSpace, hm.Used)
                ret = ret + fmt.Sprintf("%s_heap_memory{type=\"max\"} %g\n", nameSpace, hm.Max)
                ret = ret + fmt.Sprintf("%s_heap_memory{type=\"total\"} %g\n", nameSpace, hm.Total)
                ret = ret + fmt.Sprintf("%s_heap_memory{type=\"free\"} %g\n", nameSpace, hm.Free)
        }
        ret = ret + detailInfo()

        g_lock.Lock()
        g_ret = ret
        g_lock.Unlock()

        g_doing = false
}

func main() {
        flag.Parse()

        if solrUrl == nil {
                panic("error status url")
        }

        g_doing = false
        doWork()
        c := cron.New()
        c.AddFunc("0 */2 * * * ?", doWork)
        c.Start()

        mux := http.NewServeMux()
        mux.HandleFunc(*metricsPath, metrics)
        mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
                w.Write([]byte(`<html>
             <head><title>Solr Exporter</title></head>
             <body>
             <h1>Solr Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
        })
        server := http.Server{
                Handler: mux, // http.DefaultServeMux,
        }
        os.Remove(*listenAddress)

        listener, err := net.Listen("unix", *listenAddress)
        if err != nil {
                panic(err)
        }
        server.Serve(listener)
}
