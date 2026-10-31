package api

import (
	"net/http"
	"path/filepath"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/sysinfo"
)

// bridgeCollector implements prometheus.Collector by reading existing atomic
// counters from the bridge subsystems on each scrape. No dual-accounting —
// all metrics are already tracked internally; this just exposes them.
type bridgeCollector struct {
	gwManager  *gateway.Manager
	dispatcher *engine.Dispatcher
	transforms *engine.TransformPipeline
	db         *database.DB
	dbPath     string

	// System
	sysCPU    *prometheus.Desc
	sysMem    *prometheus.Desc
	sysDisk   *prometheus.Desc
	sysUptime *prometheus.Desc

	// Gateways (labelled by type and instance_id)
	gwMsgsIn    *prometheus.Desc
	gwMsgsOut   *prometheus.Desc
	gwErrors    *prometheus.Desc
	gwConnected *prometheus.Desc
	gwDLQ       *prometheus.Desc

	// Delivery loop prevention
	dlvHopDrops     *prometheus.Desc
	dlvVisitedDrops *prometheus.Desc
	dlvSelfDrops    *prometheus.Desc
	dlvDedups       *prometheus.Desc

	// FEC
	fecEncOK    *prometheus.Desc
	fecEncFail  *prometheus.Desc
	fecDecOK    *prometheus.Desc
	fecDecFail  *prometheus.Desc
	fecRecoverd *prometheus.Desc

	// Messages
	msgsTotal *prometheus.Desc
	msgsToday *prometheus.Desc
}

func newBridgeCollector(gwMgr *gateway.Manager, disp *engine.Dispatcher, tp *engine.TransformPipeline, db *database.DB, dbPath string) *bridgeCollector {
	ns := "meshsat"
	return &bridgeCollector{
		gwManager:  gwMgr,
		dispatcher: disp,
		transforms: tp,
		db:         db,
		dbPath:     dbPath,

		// System
		sysCPU:    prometheus.NewDesc(ns+"_system_cpu_percent", "Host CPU utilization (0-100).", nil, nil),
		sysMem:    prometheus.NewDesc(ns+"_system_memory_percent", "Host memory utilization (0-100).", nil, nil),
		sysDisk:   prometheus.NewDesc(ns+"_system_disk_percent", "Host disk utilization (0-100).", nil, nil),
		sysUptime: prometheus.NewDesc(ns+"_system_uptime_seconds", "Host uptime in seconds.", nil, nil),

		// Gateways
		gwMsgsIn:    prometheus.NewDesc(ns+"_gateway_messages_in_total", "Total inbound messages per gateway.", []string{"type", "instance_id"}, nil),
		gwMsgsOut:   prometheus.NewDesc(ns+"_gateway_messages_out_total", "Total outbound messages per gateway.", []string{"type", "instance_id"}, nil),
		gwErrors:    prometheus.NewDesc(ns+"_gateway_errors_total", "Total errors per gateway.", []string{"type", "instance_id"}, nil),
		gwConnected: prometheus.NewDesc(ns+"_gateway_connected", "Gateway connection state (1=connected, 0=disconnected).", []string{"type", "instance_id"}, nil),
		gwDLQ:       prometheus.NewDesc(ns+"_gateway_dlq_pending", "Messages pending in dead letter queue.", []string{"type", "instance_id"}, nil),

		// Delivery
		dlvHopDrops:     prometheus.NewDesc(ns+"_delivery_hop_limit_drops_total", "Messages dropped due to max hop count.", nil, nil),
		dlvVisitedDrops: prometheus.NewDesc(ns+"_delivery_visited_set_drops_total", "Deliveries skipped by visited-set check.", nil, nil),
		dlvSelfDrops:    prometheus.NewDesc(ns+"_delivery_self_loop_drops_total", "Deliveries skipped by self-loop check.", nil, nil),
		dlvDedups:       prometheus.NewDesc(ns+"_delivery_dedups_total", "Deliveries suppressed by content-hash dedup.", nil, nil),

		// FEC
		fecEncOK:    prometheus.NewDesc(ns+"_fec_encode_ok_total", "Successful FEC encodes.", nil, nil),
		fecEncFail:  prometheus.NewDesc(ns+"_fec_encode_fail_total", "Failed FEC encodes.", nil, nil),
		fecDecOK:    prometheus.NewDesc(ns+"_fec_decode_ok_total", "Successful FEC decodes.", nil, nil),
		fecDecFail:  prometheus.NewDesc(ns+"_fec_decode_fail_total", "Failed FEC decodes.", nil, nil),
		fecRecoverd: prometheus.NewDesc(ns+"_fec_shards_recovered_total", "Total Reed-Solomon shards recovered.", nil, nil),

		// Messages
		msgsTotal: prometheus.NewDesc(ns+"_messages_total", "Total messages in database.", nil, nil),
		msgsToday: prometheus.NewDesc(ns+"_messages_today", "Messages received today.", nil, nil),
	}
}

// Describe sends all metric descriptors to the channel.
func (c *bridgeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.sysCPU
	ch <- c.sysMem
	ch <- c.sysDisk
	ch <- c.sysUptime

	ch <- c.gwMsgsIn
	ch <- c.gwMsgsOut
	ch <- c.gwErrors
	ch <- c.gwConnected
	ch <- c.gwDLQ

	ch <- c.dlvHopDrops
	ch <- c.dlvVisitedDrops
	ch <- c.dlvSelfDrops
	ch <- c.dlvDedups

	ch <- c.fecEncOK
	ch <- c.fecEncFail
	ch <- c.fecDecOK
	ch <- c.fecDecFail
	ch <- c.fecRecoverd

	ch <- c.msgsTotal
	ch <- c.msgsToday
}

// Collect reads current values from all subsystems and emits them.
func (c *bridgeCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectSystem(ch)
	c.collectGateways(ch)
	c.collectDelivery(ch)
	c.collectFEC(ch)
	c.collectMessages(ch)
}

func (c *bridgeCollector) collectSystem(ch chan<- prometheus.Metric) {
	diskPath := "/"
	if c.dbPath != "" {
		diskPath = filepath.Dir(c.dbPath)
	}
	sys := sysinfo.Collect(diskPath)
	ch <- prometheus.MustNewConstMetric(c.sysCPU, prometheus.GaugeValue, sys.CPUPct)
	ch <- prometheus.MustNewConstMetric(c.sysMem, prometheus.GaugeValue, sys.MemPct)
	ch <- prometheus.MustNewConstMetric(c.sysDisk, prometheus.GaugeValue, sys.DiskPct)
	ch <- prometheus.MustNewConstMetric(c.sysUptime, prometheus.GaugeValue, sys.Uptime.Seconds())
}

func (c *bridgeCollector) collectGateways(ch chan<- prometheus.Metric) {
	if c.gwManager == nil {
		return
	}
	for _, gw := range c.gwManager.GetStatus() {
		labels := []string{gw.Type, gw.InstanceID}
		ch <- prometheus.MustNewConstMetric(c.gwMsgsIn, prometheus.CounterValue, float64(gw.MessagesIn), labels...)
		ch <- prometheus.MustNewConstMetric(c.gwMsgsOut, prometheus.CounterValue, float64(gw.MessagesOut), labels...)
		ch <- prometheus.MustNewConstMetric(c.gwErrors, prometheus.CounterValue, float64(gw.Errors), labels...)
		connected := 0.0
		if gw.Connected {
			connected = 1.0
		}
		ch <- prometheus.MustNewConstMetric(c.gwConnected, prometheus.GaugeValue, connected, labels...)
		ch <- prometheus.MustNewConstMetric(c.gwDLQ, prometheus.GaugeValue, float64(gw.DLQPending), labels...)
	}
}

func (c *bridgeCollector) collectDelivery(ch chan<- prometheus.Metric) {
	if c.dispatcher == nil {
		return
	}
	lm := c.dispatcher.LoopMetrics()
	snap := lm.Snapshot()
	ch <- prometheus.MustNewConstMetric(c.dlvHopDrops, prometheus.CounterValue, float64(snap["hop_limit_drops"]))
	ch <- prometheus.MustNewConstMetric(c.dlvVisitedDrops, prometheus.CounterValue, float64(snap["visited_set_drops"]))
	ch <- prometheus.MustNewConstMetric(c.dlvSelfDrops, prometheus.CounterValue, float64(snap["self_loop_drops"]))
	ch <- prometheus.MustNewConstMetric(c.dlvDedups, prometheus.CounterValue, float64(snap["delivery_dedups"]))
}

func (c *bridgeCollector) collectFEC(ch chan<- prometheus.Metric) {
	if c.transforms == nil {
		return
	}
	fec := c.transforms.FECStats()
	ch <- prometheus.MustNewConstMetric(c.fecEncOK, prometheus.CounterValue, float64(fec.EncodeOK.Load()))
	ch <- prometheus.MustNewConstMetric(c.fecEncFail, prometheus.CounterValue, float64(fec.EncodeFail.Load()))
	ch <- prometheus.MustNewConstMetric(c.fecDecOK, prometheus.CounterValue, float64(fec.DecodeOK.Load()))
	ch <- prometheus.MustNewConstMetric(c.fecDecFail, prometheus.CounterValue, float64(fec.DecodeFail.Load()))
	ch <- prometheus.MustNewConstMetric(c.fecRecoverd, prometheus.CounterValue, float64(fec.ShardsRecovered.Load()))
}

func (c *bridgeCollector) collectMessages(ch chan<- prometheus.Metric) {
	if c.db == nil {
		return
	}
	stats, err := c.db.GetMessageStats()
	if err != nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.msgsTotal, prometheus.GaugeValue, float64(stats.Total))
	ch <- prometheus.MustNewConstMetric(c.msgsToday, prometheus.GaugeValue, float64(stats.Today))
}

// newMetricsHandler creates a dedicated Prometheus registry with the bridge
// collector and returns an HTTP handler that serves the /metrics endpoint.
func newMetricsHandler(gwMgr *gateway.Manager, disp *engine.Dispatcher, tp *engine.TransformPipeline, db *database.DB, dbPath string) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(newBridgeCollector(gwMgr, disp, tp, db, dbPath))
	reg.MustRegister(newOOBCollector()) // [MESHSAT-756]
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
