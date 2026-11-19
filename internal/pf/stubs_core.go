package pf

type MetricsOptions struct{ Pool1, Pool2, NetIface, HostdSocket string }
type MetricsCollector struct{}

func NewMetricsCollector(path string, days int64, o MetricsOptions) *MetricsCollector {
	return &MetricsCollector{}
}
func StartScheduler(hc *HostClient, db *ConfigDB) {}
func RunHostd() error                             { select {} }
