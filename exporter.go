package main

import (
	"fmt"
	"os"
	"path"
	"time"
	"sync"
	"io"
	"net"
	"strings"
	"errors"
	"syscall"
	"os/signal"
	"os/exec"
	"net/http"

	"github.com/BurntSushi/toml"
	"github.com/sirupsen/logrus"
	"github.com/gorilla/mux"
	"github.com/jimdn/gomonitor"
)


type appConfig struct {
	// Log param
	LogPath            string
	LogLevel           int
	// IP:Port or :Port
	ListenAddr         string
	LanIpPrefix        []string
	SvrMonitorFile     string
}

type appVariable struct {
	Log        *logrus.Logger
	HttpSvr    *http.Server
	LocalIp    string
	// Time(ms) for update cache
	AttrTime   int64
	// Cache for last minute 
	// key   : attrId
	// value : AttrValue
	AttrMap    sync.Map
}

// value contains a type(t) and value(v)
// type : 0-counter 1-gauge
type AttrValue struct {
	t int
	v int64
}

var (
	appCfg    appConfig
	appVar    appVariable
	appFiniCb []func()
)

func init() {
	appFiniCb = make([]func(), 0)
	appVar.AttrTime = 0
}

func appInit() error {
	// configuration set default & parse from file
	appCfg.LogPath = "/data/log/moni-exporter"
	appCfg.LogLevel = 3
	appCfg.ListenAddr = ":9108"
	appCfg.LanIpPrefix = []string{"9.", "10.", "100.", "172.", "192."}
	appCfg.SvrMonitorFile = "../tools/svrmonitor.py"
	cfgFile := "../conf/moni-exporter.toml"
	if len(os.Args) >= 2 {
		cfgFile = os.Args[1]
	}
	if _, err := toml.DecodeFile(cfgFile, &appCfg); err != nil {
		fmt.Printf("DecodeFile err: %v\n", err)
		return err
	}

	// get lan if ip
	ip, err := getLocalIp()
	if err != nil {
		fmt.Printf("Error getLocalIp: %v\n", err)
		return err
	}
	appVar.LocalIp = ip

	// log init
	name := path.Base(os.Args[0])
	logPath := path.Clean(appCfg.LogPath)
	logFile := fmt.Sprintf("%s/%s.log", logPath, name)
	if err := os.MkdirAll(logPath, 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		fmt.Printf("OpenFile err: %v\n", err)
		return err
	}
	logger := logrus.New()
	logger.SetNoLock()
	logger.SetLevel(logrus.AllLevels[appCfg.LogLevel])
	logger.Out = file
	appVar.Log = logger

	// rounter
	router := mux.NewRouter()
	router.HandleFunc("/metrics", HandleMetrics)
	appVar.HttpSvr = &http.Server{
		Addr:         appCfg.ListenAddr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		Handler:      router,
	}
	return nil
}

func appFini() {
	for len(appFiniCb) > 0 {
		appFiniCb[len(appFiniCb)-1]()
		appFiniCb = appFiniCb[:len(appFiniCb)-1]
	}
}

func getLocalIp() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ipnet.IP.To4() != nil {
				ip := ipnet.IP.String()
				for _, prefix := range appCfg.LanIpPrefix {
					if strings.HasPrefix(ip, prefix) {
						return ip, nil
					}
				}
			}
		}
	}
	err = errors.New("no found")
	return "", err
}

func updateAttrCache(ms int64) {
	// update time
	appVar.AttrTime = ms
	// update value (delete first and update next)
	appVar.AttrMap.Range(func(k, v interface{}) bool {
		appVar.AttrMap.Delete(k)
		return true
	})
	row := gomonitor.MaxRow()
	col := gomonitor.MaxCol()
	for rowIdx := 0; rowIdx < row; rowIdx++ {
		for colIdx := 0; colIdx < col; colIdx++ {
			id, t, v := gomonitor.AttrWalk(rowIdx, colIdx)
			if id > 0 && t >= 0 && v >= 0 {
				attrVal := AttrValue {
					t: t,
					v: v,
				}
				appVar.AttrMap.Store(id, attrVal)
			}
		}
	}
}

// HandleMetrics:
// HTTP endpoints for Prometheus
func HandleMetrics(w http.ResponseWriter, r *http.Request) {
	appVar.Log.Infof("receive req from %s", r.RemoteAddr)

	// update cache every minute
	now := time.Now().Unix()
	ms := (now / 60) * 60 * 1000
	if ms > appVar.AttrTime {
		updateAttrCache(ms)
	}

	reportData := ""
	// monitor_0_value used to report local ip
	tmp := fmt.Sprintf("# HELP monitor_0_value n/a\n# TYPE monitor_0_value gauge\nmonitor_0_value{host=\"%s\"} 1\n", appVar.LocalIp)
	reportData += tmp

	appVar.AttrMap.Range(func(k, v interface{}) bool {
		key, _ := k.(int)
		val, _ := v.(AttrValue)
		switch val.t {
		case 0:
			// counter
			metricName := fmt.Sprintf("monitor_%d_total", key)
			helpStr := fmt.Sprintf("# HELP %s n/a\n", metricName)
			typeStr := fmt.Sprintf("# TYPE %s counter\n", metricName)
			valStr := fmt.Sprintf("%s{host=\"%s\"} %d %d\n", metricName, appVar.LocalIp, val.v, appVar.AttrTime)
			reportData += fmt.Sprintf("%s%s%s", helpStr, typeStr, valStr)
		case 1:
			// gauge
			metricName := fmt.Sprintf("monitor_%d_value", key)
			helpStr := fmt.Sprintf("# HELP %s n/a\n", metricName)
			typeStr := fmt.Sprintf("# TYPE %s gauge\n", metricName)
			valStr := fmt.Sprintf("%s{host=\"%s\"} %d %d\n", metricName, appVar.LocalIp, val.v, appVar.AttrTime)
			reportData += fmt.Sprintf("%s%s%s", helpStr, typeStr, valStr)
		}
		return true
	})
	io.WriteString(w, reportData)
}

// MonitorServer:
// run script to monitor server base
// like network, cpu, mem, ...
func MonitorServer() {
	minute := (time.Now().Unix() / 60) * 60
	for {
		// exec every minute
		now := time.Now().Unix()
		if now - minute == 60 {
			minute = now
			cmd := exec.Command("/usr/bin/env", "python", appCfg.SvrMonitorFile)
			err := cmd.Start()
			if err != nil {
				appVar.Log.Warnf("cmd exec failed, err=%v", err)
			}
		}
	}
}


func main() {
	defer appFini()
	if err := appInit(); err != nil {
		fmt.Printf("appInit fail: %v\n", err)
		return
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		switch sig := <-sigs; sig {
		default:
			{
				appVar.Log.Warnf("receive exit signal!")
				appVar.HttpSvr.Shutdown(nil)
			}
		}
	}()

	go MonitorServer()

	appVar.Log.Warnf("moni-exporter | server restart!")

	if err := appVar.HttpSvr.ListenAndServe(); err != nil {
		appVar.Log.Warnf("ListenAndServe err=%v", err)
	}

	appVar.Log.Warnf("moni-exporter | server stop!")
	return
}
