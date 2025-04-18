package push

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"go.acuvity.ai/bahamut"
)

// A Notifier sends ServicePing to the Wutai gateways.
type Notifier struct {
	pubsub             bahamut.PubSubClient
	limiters           IdentityToAPILimitersRegistry
	privateOverrides   map[string]bool
	hiddenAPIs         map[string]bool
	serviceName        string
	endpoint           string
	serviceStatusTopic string
	prefix             string
	frequency          time.Duration
}

// NewNotifier returns a new Wutai notifier.
func NewNotifier(
	pubsub bahamut.PubSubClient,
	serviceStatusTopic string,
	serviceName string,
	endpoint string,
	opts ...NotifierOption,
) *Notifier {

	cfg := newNotifierConfig()
	for _, o := range opts {
		o(&cfg)
	}

	return &Notifier{
		pubsub:             pubsub,
		serviceName:        serviceName,
		endpoint:           endpoint,
		serviceStatusTopic: serviceStatusTopic,
		limiters:           cfg.rateLimits,
		frequency:          cfg.pingInterval,
		prefix:             cfg.prefix,
		privateOverrides:   cfg.privateOverrides,
		hiddenAPIs:         cfg.hiddenAPIs,
	}
}

// MakeStartHook returns a bahamut start hook that sends the hello message to the Upstreamer periodically.
func (w *Notifier) MakeStartHook(ctx context.Context) func(server bahamut.Server) error {

	return func(server bahamut.Server) error {

		p, err := process.NewProcess(int32(os.Getpid()))
		if err != nil {
			return err
		}

		routes := map[int][]bahamut.RouteInfo{}
		pristineRoutes := server.RoutesInfo()
		for v, versionedRoutes := range pristineRoutes {
			for i, r := range versionedRoutes {
				hidden, ok := w.hiddenAPIs[r.Identity]
				if ok && hidden {
					continue
				}

				priv, ok := w.privateOverrides[r.Identity]
				if ok {
					r.Private = priv
					versionedRoutes[i] = r
				}

				routes[v] = append(routes[v], r)
			}
		}

		sp := servicePing{
			Name:         w.serviceName,
			Prefix:       w.prefix,
			Status:       entityStatusHello,
			Endpoint:     w.endpoint,
			Routes:       routes,
			Versions:     server.VersionsInfo(),
			PushEndpoint: server.PushEndpoint(),
			APILimiters:  w.limiters,
		}

		pct, err := p.CPUPercent()
		if err != nil {
			return err
		}

		// Use the maxproc to get a percentage between 0 and 100
		cores := float64(runtime.GOMAXPROCS(0))

		sp.Load = pct / cores

		pub := bahamut.NewPublication(w.serviceStatusTopic)
		if err := pub.Encode(sp); err != nil {
			return err
		}

		if err := w.pubsub.Publish(pub); err != nil {
			return err
		}

		go func() {
			for {
				select {
				case <-time.After(w.frequency):

					if pct, err = p.Percent(0); err != nil {
						slog.Error("Unable to retrieve cpu usage", err)
						continue
					}

					sp.Load = pct / cores

					if err := pub.Encode(sp); err != nil {
						slog.Error("Unable to encode service ping", err)
						continue
					}

					if err := w.pubsub.Publish(pub); err != nil {
						slog.Error("Unable to send wutai up ping", err)
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		return nil
	}
}

// MakeStopHook returns a bahamut stop hook that sends the goodbye message to the Upstreamer.
func (w *Notifier) MakeStopHook() func(server bahamut.Server) error {

	return func(server bahamut.Server) error {

		pub := bahamut.NewPublication(w.serviceStatusTopic)
		if err := pub.Encode(servicePing{
			Name:     w.serviceName,
			Prefix:   w.prefix,
			Status:   entityStatusGoodbye,
			Endpoint: w.endpoint,
		}); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := w.pubsub.Publish(pub, bahamut.NATSOptPublishRequireAck(ctx)); err != nil {
			return err
		}

		<-time.After(time.Second)

		return w.pubsub.Disconnect()
	}
}
