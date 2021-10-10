package events

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sync"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

type ObservableEventType int

const (
	SettledInvoice ObservableEventType = iota
	Forward
	eventLimit
)

func (e ObservableEventType) String() string {
	strings := [...]string{"SettleInvoice", "Forward"}

	// prevent panicking in case of status is out-of-range
	if e < SettledInvoice || e > Forward {
		return "Unknown"
	}

	return strings[e]
}

type Observable interface {
	Register(observer *Observer, e ObservableEventType)
	Deregister(observer *Observer, e ObservableEventType)
	Start()
	UpdateAll()
}

type LNDEventListener struct{}

type Observer interface {
	Update(event *Event)
	GetName() string
}

type Event struct {
	Type ObservableEventType
	// Forward fields
	FromPubKey    string
	FromAlias     string
	IncomingMSats uint64
	ToAlias       string
	ToPubKey      string
	OutgoingMSats uint64
	ChanId_In     uint64
	ChanId_Out    uint64
	HtlcId_In     uint64
	HtlcId_Out    uint64
	// Invoice fields
	IsSettled         bool
	SettleAmount_msat int64
	Preimage          []uint8
}

type Config struct {
	MacaroonPath string
	CertPath     string
	RpcHost      string
}

var observers map[ObservableEventType][]Observer

var forwardsInFlight map[uint64]*routerrpc.HtlcEvent

var router routerrpc.RouterClient

var lndcli lnrpc.LightningClient

var thisNodesPubKey string

// Reads lnd config parameters
// Creates a new instance of router event listener that observers can subscribe to
func New(config *Config) *LNDEventListener {
	observers = make(map[ObservableEventType][]Observer)
	forwardsInFlight = make(map[uint64]*routerrpc.HtlcEvent)

	macaroonBytes, err := ioutil.ReadFile(config.MacaroonPath)
	if err != nil {
		log.Fatal("Cannot read macaroon file", err)
	}

	mac := &macaroon.Macaroon{}
	if err = mac.UnmarshalBinary(macaroonBytes); err != nil {
		log.Fatal("Cannot unmarshal macaroon", err)
	}

	err = os.Setenv("GRPC_SSL_CIPHER_SUITES", "HIGH+ECDSA")
	if err != nil {
		log.Fatal("Cannot set environment variable GRPC_SSL_CIPHER_SUITES")
	}

	creds, err := credentials.NewClientTLSFromFile(config.CertPath, "")
	if err != nil {
		log.Fatal("Cannot load credentials from CertPath: %s", config.CertPath)
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(macaroons.NewMacaroonCredential(mac)),
	}

	conn, err := grpc.Dial(config.RpcHost, opts...)
	if err != nil {
		log.Fatalf("Cannot connect to %s with cert %s\n", config.RpcHost, config.CertPath)
	}

	router = routerrpc.NewRouterClient(conn)
	lndcli = lnrpc.NewLightningClient(conn)
	info, err := lndcli.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})

	if err != nil {
		log.Fatal("Could not retrieve this node's pub key %#v", err)
	}
	thisNodesPubKey = info.IdentityPubkey

	return &LNDEventListener{}
}

func (r *LNDEventListener) Start() {

	var wg sync.WaitGroup

	for e := ObservableEventType(0); e < eventLimit; e++ {
		_, exists := observers[e]
		if exists {
			wg.Add(1)
			switch e {
			case SettledInvoice:
				go r.subscribeInvoiceSettlements()
			case Forward:
				go r.subscribeHtlcEvents()
			default:
			}
		}
	}

	wg.Wait()
}

func (r *LNDEventListener) subscribeHtlcEvents() {

	events, err := router.SubscribeHtlcEvents(context.Background(), &routerrpc.SubscribeHtlcEventsRequest{})
	if err != nil {
		log.Fatalf("Cannot subscribe to Htlc events: %#v\n", err)
	}

	log.Println("Listening for Htlc events...")
	for {
		event, err := events.Recv()
		if err != nil {
			log.Println("got error from events.Recv()", err)
			return
		}

		// calculate key for in flight forward events to identify settlement details
		inFlightKey := event.IncomingChannelId + event.OutgoingChannelId + event.IncomingHtlcId + event.OutgoingHtlcId

		switch event.Event.(type) {
		case *routerrpc.HtlcEvent_SettleEvent:
			e, exists := forwardsInFlight[inFlightKey]
			if !exists {
				continue
			}

			delete(forwardsInFlight, inFlightKey)

			settleEvent := settleEventDetails(e)
			r.UpdateAll(settleEvent)

		case *routerrpc.HtlcEvent_LinkFailEvent:
			delete(forwardsInFlight, inFlightKey)
		case *routerrpc.HtlcEvent_ForwardFailEvent:
			delete(forwardsInFlight, inFlightKey)
		case *routerrpc.HtlcEvent_ForwardEvent:
			forwardsInFlight[inFlightKey] = event
		}
		log.Printf("Size of inflight forward map: %d\n", len(forwardsInFlight))
	}
}

func (r *LNDEventListener) subscribeInvoiceSettlements() {

	req := &lnrpc.InvoiceSubscription{}

	ctx, cancelInvoiceSubscription := context.WithCancel(context.Background())

	defer cancelInvoiceSubscription()

	invoiceSubscription, err := lndcli.SubscribeInvoices(ctx, req)

	if err != nil {
		log.Fatalf("Cannot subscribe to invoices: %#v\n", err)
	}

	log.Println("Listening for invoice events...")
	for {
		invoiceUpdate, err := invoiceSubscription.Recv()
		if err != nil {
			log.Println("got error from events.Recv()", err)
			return
		}
		log.Printf("Invoice update: %#v\n", invoiceUpdate)
		r.UpdateAll(&Event{
			Type:              SettledInvoice,
			IsSettled:         invoiceUpdate.Settled,
			SettleAmount_msat: invoiceUpdate.AmtPaidMsat,
			Preimage:          invoiceUpdate.RPreimage,
		})
	}

}

func settleEventDetails(event *routerrpc.HtlcEvent) *Event {

	var fromAlias, toAlias, fromPubKey, toPubKey string

	incomingChanInfo, err := lndcli.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: event.IncomingChannelId})

	if err != nil {
		log.Println("Cannot get incoming channel info", err)
		fromPubKey = "Incoming pub key not available"
		fromAlias = "Info not available"
	} else {
		if incomingChanInfo.Node1Pub == thisNodesPubKey {
			fromAlias = fmt.Sprintf("%s", getNodeAlias(incomingChanInfo.Node2Pub))
			fromPubKey = incomingChanInfo.Node2Pub
		} else {
			fromAlias = fmt.Sprintf("%s", getNodeAlias(incomingChanInfo.Node1Pub))
			fromPubKey = incomingChanInfo.Node1Pub
		}
	}

	outgoingChanInfo, err := lndcli.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: event.OutgoingChannelId})

	if err != nil {
		log.Println("Cannot get outgoing channel info", err)
		toPubKey = "Outgoing pub key not available"
		toAlias = "Nowhere - you've been paid"
	} else {
		if outgoingChanInfo.Node1Pub == thisNodesPubKey {
			toPubKey = outgoingChanInfo.Node2Pub
			toAlias = fmt.Sprintf("%s", getNodeAlias(outgoingChanInfo.Node2Pub))
		} else {
			toPubKey = outgoingChanInfo.Node1Pub
			toAlias = fmt.Sprintf("%s", getNodeAlias(outgoingChanInfo.Node1Pub))
		}
	}

	return &Event{
		Type:          Forward,
		FromPubKey:    fromPubKey,
		FromAlias:     fromAlias,
		ToPubKey:      toPubKey,
		ToAlias:       toAlias,
		IncomingMSats: event.GetForwardEvent().Info.IncomingAmtMsat,
		OutgoingMSats: event.GetForwardEvent().Info.OutgoingAmtMsat,
	}
}

func (r *LNDEventListener) Register(o Observer, t ObservableEventType) {
	log.Printf("Registering observer %s for %s events\n", o.GetName(), t.String())
	observers[t] = append(observers[t], o)
}

func (r *LNDEventListener) Deregister(o Observer, t ObservableEventType) {
	log.Printf("Deregistering observer %s for %s events\n", o.GetName(), t.String())
	_, exists := observers[t]
	if exists {
		delete(observers, t)
	} else {
		log.Printf("Cannot deregister. EventConsumer does not exists")
	}
}

func (r *LNDEventListener) UpdateAll(event *Event) {
	for _, o := range observers[event.Type] {
		o.Update(event)
	}
}

func getNodeAlias(pubKey string) string {

	nodeInfo, err := lndcli.GetNodeInfo(context.Background(), &lnrpc.NodeInfoRequest{
		PubKey: pubKey,
	})
	if err != nil {
		log.Printf("Cannot retrieve node info for pubkey %s\n", pubKey)
	}
	return nodeInfo.Node.Alias
}
