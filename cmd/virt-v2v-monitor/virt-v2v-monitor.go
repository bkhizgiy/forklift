package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"k8s.io/klog/v2"
)

var COPY_DISK_RE = regexp.MustCompile(`^.*Copying disk (\d+)/(\d+)`)
var DISK_PROGRESS_RE = regexp.MustCompile(`\s+\((\d+).*|.+ (\d+)% \[[*-]+\]`)
var FINISHED_RE = regexp.MustCompile(`^\[[ .0-9]*\] Finishing off`)

// Here is a scan function that imposes limit on returned line length. virt-v2v
// writes some overly long lines that don't fit into the internal buffer of
// Scanner. We could just provide bigger buffer, but it is hard to guess what
// size is large enough. Instead we just claim that line ends when it reaches
// buffer size.
func customScanLines(r io.Reader) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		buf := make([]byte, 64*1024)
		var line []byte
		for {
			n, err := r.Read(buf)
			if err != nil && err != io.EOF {
				fmt.Println("Error reading input:", err)
				return
			}
			if n == 0 {
				break
			}
			for _, b := range buf[:n] {
				if b == '\n' {
					out <- string(line)
					line = nil
				} else {
					line = append(line, b)
				}
			}
		}
		if len(line) > 0 {
			out <- string(line)
		}
	}()
	return out
}

func updateProgress(progressCounter *prometheus.CounterVec, disk, progress uint64) (err error) {
	if disk == 0 {
		return
	}

	label := strconv.FormatUint(disk, 10)

	var m = &dto.Metric{}
	if err = progressCounter.WithLabelValues(label).Write(m); err != nil {
		return
	}
	previous_progress := m.Counter.GetValue()

	change := float64(progress) - previous_progress
	if change > 0 {
		klog.Infof("Progress changed for disk %d about %v", disk, change)
		progressCounter.WithLabelValues(label).Add(change)
	}
	return
}

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()
	flag.Parse()

	// Start prometheus metrics HTTP handler
	klog.Info("Setting up prometheus endpoint :2112/metrics")
	klog.Info("this is Bella test")
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(":2112", nil)

	progressCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "v2v",
			Name:      "disk_transfers",
			Help:      "Percent of disk copied",
		},
		[]string{"disk_id"},
	)
	if err := prometheus.Register(progressCounter); err != nil {
		// Exit gracefully if we fail here. We don't need monitoring
		// failures to hinder guest conversion.
		klog.Error("Prometheus progress counter not registered:", err)
		return
	}
	klog.Info("Prometheus progress counter registered.")

	var diskNumber uint64 = 0
	var disks uint64 = 0
	var progress uint64 = 0

	scanner := customScanLines(os.Stdin)
	for line := range scanner {
		fmt.Println("This is the line we are scanning now:", line)
		var err error

		if match := COPY_DISK_RE.FindStringSubmatch(line); match != nil {
			diskNumber, _ = strconv.ParseUint(string(match[1]), 10, 0)
			disks, _ = strconv.ParseUint(string(match[2]), 10, 0)
			klog.Infof("Copying disk %d out of %d", diskNumber, disks)
			progress = 0
			err = updateProgress(progressCounter, diskNumber, progress)
		} else if match := DISK_PROGRESS_RE.FindStringSubmatch(line); match != nil {
			klog.Info("we are here at progress ", line)
			progress, _ = strconv.ParseUint(string(match[1]), 10, 0)
			klog.Infof("Progress update, completed %d %%", progress)
			err = updateProgress(progressCounter, diskNumber, progress)
		} else if match := FINISHED_RE.FindStringSubmatch(line); match != nil {
			// Make sure we flag conversion as finished. This is
			// just in case we miss the last progress update for some reason.
			klog.Infof("Finished")
			for disk := uint64(0); disk < disks; disk++ {
				err = updateProgress(progressCounter, disk, 100)
			}
		} else {
			klog.Infof("Ignoring line: ", string(line))
		}
		if err != nil {
			// Don't make processing errors fatal.
			klog.Error("Error updating progress: ", err)
			err = nil
		}
	}
}
