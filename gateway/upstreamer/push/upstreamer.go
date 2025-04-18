package push

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/uuid"
	"go.acuvity.ai/bahamut"
	"go.acuvity.ai/bahamut/gateway"
	"golang.org/x/time/rate"
)

var emptyRateSet = &rateSet{}

type rateSet struct {
	limit rate.Limit
	burst int
}

// A Upstreamer listens and update the
// list of the backend services.
// It also implement gateway.Limiter interface
// allowing to install the per token rate limiter
// in an efficient way.
type Upstreamer struct {
	lock               sync.RWMutex
	latencies          sync.Map
	lastPeerChangeDate atomic.Value
	lastRateSet        atomic.Value
	pubsub             bahamut.PubSubClient
	apis               map[string][]*endpointInfo
	serviceStatusTopic string
	peerStatusTopic    string
	config             upstreamConfig
	peersCount         int64
}

// NewUpstreamer returns a new push backed upstreamer latency based
func NewUpstreamer(
	pubsub bahamut.PubSubClient,
	serviceStatusTopic string,
	peerStatusTopic string,
	options ...UpstreamerOption,
) *Upstreamer {

	cfg := newUpstreamConfig()
	for _, opt := range options {
		opt(&cfg)
	}

	return &Upstreamer{
		pubsub:             pubsub,
		apis:               map[string][]*endpointInfo{},
		serviceStatusTopic: serviceStatusTopic,
		peerStatusTopic:    peerStatusTopic,
		config:             cfg,
	}
}

// ExtractRates implements the gateway.Limiter interface.
func (c *Upstreamer) ExtractRates(r *http.Request) (rate.Limit, int, error) {

	rl, ok := c.lastRateSet.Load().(*rateSet)

	if !ok || rl == emptyRateSet {

		currentPeers := atomic.LoadInt64(&c.peersCount) + 1 // that's us!
		rl = &rateSet{
			limit: c.config.tokenLimitingRPS / rate.Limit(currentPeers),
			burst: c.config.tokenLimitingBurst / int(currentPeers),
		}

		c.lastRateSet.Store(rl)
	}

	return rl.limit, rl.burst, nil
}

// Upstream returns the upstream to go for the given path
func (c *Upstreamer) Upstream(req *http.Request) (string, error) {

	identity, prefix := getTargetIdentity(req.URL.Path)
	key := fmt.Sprintf("%s/%s", prefix, identity)

	// we rewrite the request to trim the prefix out.
	req.URL.Path = strings.TrimPrefix(req.URL.Path, "/_"+prefix)
	req.RequestURI = req.URL.String()

	c.lock.RLock()
	defer c.lock.RUnlock()

	l := len(c.apis[key])

	var n1, n2 int

	switch l {

	case 0:
		return "", nil

	case 1:
		ep := c.apis[key][0]
		ep.RLock()
		defer ep.RUnlock()

		return ep.address, nil

	case 2:
		n1, n2 = 0, 1

	default:
		n1, n2 = pick(c.config.randomizer, l)
	}

	epi1 := c.apis[key][n1]
	epi2 := c.apis[key][n2]

	addresses := [2]string{}
	loads := [2]float64{}
	rls := [2]*rate.Limiter{}

	var rls1NeedsLimitingUpdate, rls2NeedsLimitingUpdate bool

	// BEGIN LOCKED OPERATIONS
	epi1.RLock()
	addresses[0] = epi1.address
	loads[0] = epi1.lastLoad

	currentPeers := atomic.LoadInt64(&c.peersCount) + 1 // that's us!
	lastPeerUpdate := func() time.Time { o, _ := c.lastPeerChangeDate.Load().(time.Time); return o }()

	if epi1.limiters != nil && epi1.limiters[identity] != nil {

		rls[0] = epi1.limiters[identity].limiter

		if rls[0] != nil && epi1.lastLimiterAdjust.Before(lastPeerUpdate) {
			rls1NeedsLimitingUpdate = true
			rls[0].SetBurst(epi1.limiters[identity].Burst / int(currentPeers))
			rls[0].SetLimit(epi1.limiters[identity].Limit / rate.Limit(currentPeers))
		}
	}
	epi1.RUnlock()

	epi2.RLock()
	addresses[1] = epi2.address
	loads[1] = epi2.lastLoad

	if epi2.limiters != nil && epi2.limiters[identity] != nil {

		rls[1] = epi2.limiters[identity].limiter

		if rls[1] != nil && epi2.lastLimiterAdjust.Before(lastPeerUpdate) {
			rls2NeedsLimitingUpdate = true
			rls[1].SetBurst(epi2.limiters[identity].Burst / int(currentPeers))
			rls[1].SetLimit(epi2.limiters[identity].Limit / rate.Limit(currentPeers))
		}
	}
	epi2.RUnlock()
	// END LOCKED OPERATIONS

	if rls1NeedsLimitingUpdate {
		epi1.Lock()
		epi1.lastLimiterAdjust = lastPeerUpdate
		epi1.Unlock()
	}

	if rls2NeedsLimitingUpdate {
		epi2.Lock()
		epi2.lastLimiterAdjust = lastPeerUpdate
		epi2.Unlock()
	}

	w := [2]float64{.0, .0}

	// fill our weight from the Feedbackloop
	if ma, ok := c.latencies.Load(addresses[0]); ok {
		if v, err := ma.(movingAverage).average(); err == nil {
			w[0] = v
		}
	}

	if ma, ok := c.latencies.Load(addresses[1]); ok {
		if v, err := ma.(movingAverage).average(); err == nil {
			w[1] = v
		}
	}

	// Make sure we got an average for both
	// otherwise default to loads
	if w[0] == 0 || w[1] == 0 {
		w[0] = loads[0]
		w[1] = loads[1]
	}

	// sort
	if w[0] > w[1] {
		addresses[1], addresses[0] = addresses[0], addresses[1]
		loads[1], loads[0] = loads[0], loads[1]
		rls[1], rls[0] = rls[0], rls[1]
		w[1], w[0] = w[0], w[1]
	}

	// Compute cummulative distribution
	w[1] = w[0] + w[1]

	// Given a random choice from 0 to w[1]+1
	draw := float64(c.config.randomizer.Intn(int(w[1]) + 1))

	// routine to extract the endpoint for the given
	// choice index. If it returns false, the object
	// has a rate limiter, and it is currently full.
	check := func(idx uint8) (string, bool) {
		if rls[idx] != nil && !rls[idx].Allow() {
			return "", false
		}
		return addresses[idx], true
	}

	// We pick the fastest/less loaded candidate
	// and get the index of the winner.
	var idx uint8
	if draw <= w[0] {
		idx = 1
	}

	// We check if the winner should be
	// ok to handle the request based on its
	// requested rate limiting. If so, we return
	// it's address.
	addr, ok := check(idx)
	if ok {
		return addr, nil
	}

	// If not, we flip the index.
	if idx == 0 {
		idx = 1
	} else {
		idx = 0
	}

	// And we check if the other endpoint would
	// be ok to handle the request.
	//
	// Note: we may need to make a decision based on the difference
	// of the load between the 2 candidates.
	addr, ok = check(idx)
	if ok {
		return addr, nil
	}

	// If it is sill not ok, we return a 429 error.
	return "", gateway.ErrUpstreamerTooManyRequests
}

// Start starts for new backend services.
func (c *Upstreamer) Start(ctx context.Context) (chan struct{}, *sync.WaitGroup) {

	ready := make(chan struct{})

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.listenPeers(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.listenServices(ctx, ready)
	}()

	return ready, &wg
}

func (c *Upstreamer) listenServices(ctx context.Context, ready chan struct{}) {

	var err error

	pubs := make(chan *bahamut.Publication, 1024)
	errs := make(chan error, 1024)

	if c.config.globalServiceTopic == "" {

		// if there is no global service topic,
		// we simply subscribe to the c.serviceStatusTopic.

		unsub := c.pubsub.Subscribe(pubs, errs, c.serviceStatusTopic)
		defer unsub()

	} else {

		// If c.config.globalServiceTopic is set, we listen to both
		// topics, and multiplex them

		lpubs := make(chan *bahamut.Publication, 1024)
		lerrs := make(chan error, 1024)
		lunsub := c.pubsub.Subscribe(lpubs, lerrs, c.serviceStatusTopic)
		defer lunsub()

		gpubs := make(chan *bahamut.Publication, 1024)
		gerrs := make(chan error, 1024)
		gunsub := c.pubsub.Subscribe(gpubs, gerrs, c.config.globalServiceTopic)
		defer gunsub()

		go func() {
			for {
				select {

				case p := <-lpubs:
					pubs <- p
				case p := <-gpubs:
					pubs <- p

				case e := <-lerrs:
					errs <- e
				case e := <-gerrs:
					errs <- e

				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var requiredReady int
	var requiredNotifSent bool

	requiredCount := len(c.config.requiredServices)
	requiredServices := map[string]bool{}
	for _, srv := range c.config.requiredServices {
		requiredServices[srv] = false
	}

	if requiredCount == 0 {
		requiredNotifSent = true
		close(ready)
	}

	services := servicesConfig{}

	ticker := time.NewTicker(c.config.serviceTimeoutCheckInterval)
	defer ticker.Stop()

	for {
		select {

		case <-ticker.C:

			since := time.Now().Add(-c.config.serviceTimeout)

			var foundOutdated bool
			for _, srv := range services {
				name, prefix := extractPrefix(srv.name)
				for _, ep := range srv.outdatedEndpoints(since) {
					foundOutdated = foundOutdated || handleRemoveServicePing(
						services,
						servicePing{
							Name:     name,
							Prefix:   prefix,
							Endpoint: ep,
						},
					)
					c.latencies.Delete(ep)
					slog.Info("Handled outdated service",
						"name", srv.name,
						"backend", ep,
					)
				}
			}

			if foundOutdated {
				c.lock.Lock()
				c.apis = resyncRoutes(services, c.config.exposePrivateAPIs, c.config.eventsAPIs)
				c.lock.Unlock()
			}

		case pub := <-pubs:

			var sp servicePing

			if err = pub.Decode(&sp); err != nil {
				slog.Error("Unable to decode service ping", err)
				break
			}

			if c.config.overrideEndpointAddress != "" {
				_, p, err := net.SplitHostPort(sp.Endpoint)
				if err == nil {
					sp.Endpoint = c.config.overrideEndpointAddress + ":" + p
				}
			}

			switch sp.Status {
			case entityStatusHello:

				if handleAddServicePing(services, sp) {
					c.lock.Lock()
					c.apis = resyncRoutes(services, c.config.exposePrivateAPIs, c.config.eventsAPIs)
					c.lock.Unlock()
					slog.Debug("Handled service hello",
						"key", sp.Key(),
						"backend", sp.Endpoint,
					)
				}

				if requiredCount > 0 && !requiredNotifSent {

					if r, ok := requiredServices[sp.Name]; ok && !r {
						requiredServices[sp.Name] = true
						requiredReady++
					}

					if requiredReady == requiredCount {
						requiredNotifSent = true
						close(ready)
					}
				}

			case entityStatusGoodbye:

				if handleRemoveServicePing(services, sp) {
					c.lock.Lock()
					c.apis = resyncRoutes(services, c.config.exposePrivateAPIs, c.config.eventsAPIs)
					c.lock.Unlock()
					c.latencies.Delete(sp.Endpoint)
					slog.Debug(
						"Handled service goodbye",
						"key", sp.Key(),
						"backend", sp.Endpoint,
					)
				}
			}

		case err = <-errs:
			slog.Error("Received error from pubsub services channel", err)

		case <-ctx.Done():
			return
		}
	}
}

func (c *Upstreamer) listenPeers(ctx context.Context) {

	// Build the UUID
	uuid4, _ := uuid.NewV4()
	rid := uuid4.String()

	// Build publications.
	helloPub := bahamut.NewPublication(c.peerStatusTopic)
	_ = helloPub.Encode(peerPing{
		RuntimeID: rid,
		Status:    entityStatusHello,
	}) // no error can be returned here

	goodbyePub := bahamut.NewPublication(c.peerStatusTopic)
	_ = goodbyePub.Encode(peerPing{
		RuntimeID: rid,
		Status:    entityStatusGoodbye,
	}) // no error can be returned here

	sendTicker := time.NewTicker(c.config.peerPingInterval)
	defer sendTicker.Stop()

	cleanTicker := time.NewTicker(c.config.peerTimeoutCheckInterval)
	defer sendTicker.Stop()

	pubs := make(chan *bahamut.Publication, 1024)
	errs := make(chan error, 1024)

	unsub := c.pubsub.Subscribe(pubs, errs, c.peerStatusTopic)
	defer unsub()

	peers := sync.Map{}

	// Send the first ping immediately
	if err := c.pubsub.Publish(helloPub); err != nil {
		slog.Error("Unable to send initial hello to pubsub peers channel", err)
	}

	for {
		select {

		case <-sendTicker.C:

			if err := c.pubsub.Publish(helloPub); err != nil {
				slog.Error("Unable to send hello to pubsub peers channel", err)
			}

		case <-cleanTicker.C:

			now := time.Now()
			var deleted int64
			peers.Range(func(id, date any) bool {
				if now.After(date.(time.Time).Add(c.config.peerTimeout)) {
					peers.Delete(id)
					deleted++
				}
				return true
			})

			if deleted > 0 {
				atomic.AddInt64(&c.peersCount, -deleted)
				c.lastPeerChangeDate.Store(now)
				c.lastRateSet.Store(emptyRateSet)
			}

		case pub := <-pubs:

			var ping peerPing

			if err := pub.Decode(&ping); err != nil {
				slog.Error("Unable to decode uostream ping", err)
				break
			}

			if ping.RuntimeID == rid {
				break
			}

			switch ping.Status {
			case entityStatusHello:
				if _, ok := peers.Load(ping.RuntimeID); !ok {
					atomic.AddInt64(&c.peersCount, 1)
					c.lastPeerChangeDate.Store(time.Now())
					c.lastRateSet.Store(emptyRateSet)
				}
				peers.Store(ping.RuntimeID, time.Now())

			case entityStatusGoodbye:
				if _, ok := peers.Load(ping.RuntimeID); ok {
					peers.Delete(ping.RuntimeID)
					atomic.AddInt64(&c.peersCount, -1)
					c.lastPeerChangeDate.Store(time.Now())
					c.lastRateSet.Store(emptyRateSet)
				}
			}

		case err := <-errs:
			slog.Error("Received error from pubsub upstreams channel", err)

		case <-ctx.Done():
			if err := c.pubsub.Publish(goodbyePub); err != nil {
				slog.Error("Unable to send hello to pubsub upstreams channel", err)
			}

			return
		}
	}
}

// CollectLatency implement the LatencyBasedUpstreamer interface to add new
// samples into the latencies sync map
func (c *Upstreamer) CollectLatency(address string, responseTime time.Duration) {

	if values, ok := c.latencies.Load(address); ok {
		c.latencies.Store(address, values.(movingAverage).append(float64(responseTime.Microseconds())))
	} else {
		c.latencies.Store(address, newMovingAverage(c.config.latencySampleSize))
		c.CollectLatency(address, responseTime)
	}
}
