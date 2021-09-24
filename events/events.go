package events

import (
	"context"
	"io/ioutil"
	"log"
	"os"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

type Observable interface {
	Register(observer *Observer)
	Deregister(observer *Observer)
	Start()
	notifyAll(event *Event)
}

type RoutingListener struct{}

type Observer interface {
	Update(event *Event)
	GetName() string
}

type Event struct {
	Type          string
	FromPubKey    string
	FromAlias     string
	IncomingMSats uint64
	ToAlias       string
	ToPubKey      string
	OutgoingMSats uint64
}

type Config struct {
	MacaroonPath string
	CertPath     string
	RpcHost      string
}

var observers map[string]Observer

var router routerrpc.RouterClient

var lndcli lnrpc.LightningClient

// Reads lnd config parameters
// Creates a new instance of router event listener that observers can subscribe to
func New(config *Config) *RoutingListener {
	observers = make(map[string]Observer)

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
		log.Fatal("Cannot load credentials from CertPath: ", config.CertPath)
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

	return &RoutingListener{}
}

func (r *RoutingListener) Start() {
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

		incomingChanInfo, err := lndcli.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: event.IncomingChannelId})
		if err != nil {
			log.Println("Cannot get incoming channel info", err)
			continue
		}
		outgoingChanInfo, err := lndcli.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: event.OutgoingChannelId})
		if err != nil {
			log.Println("Cannot get outgoing channel info", err)
			continue
		}

		e := &Event{
			FromPubKey: incomingChanInfo.Node2Pub,
			FromAlias:  getNodeAlias(incomingChanInfo.Node2Pub),
			ToPubKey:   outgoingChanInfo.Node1Pub,
			ToAlias:    getNodeAlias(outgoingChanInfo.Node1Pub),
		}

		switch event.Event.(type) {
		case *routerrpc.HtlcEvent_SettleEvent:
			e.Type = "SettleEvent"
		case *routerrpc.HtlcEvent_LinkFailEvent:
			e.Type = "LinkFailEvent"
		case *routerrpc.HtlcEvent_ForwardFailEvent:
			e.Type = "ForwardFailEvent"
		case *routerrpc.HtlcEvent_ForwardEvent:
			e.Type = "ForwardEvent"
			e.IncomingMSats = event.GetForwardEvent().GetInfo().IncomingAmtMsat
			e.OutgoingMSats = event.GetForwardEvent().GetInfo().OutgoingAmtMsat
		}
		r.UpdateAll(e)
	}
}

func (r *RoutingListener) Register(o Observer) {
	log.Printf("Registering observer %s\n", o.GetName())
	observers[o.GetName()] = o
}

func (r *RoutingListener) Deregister(o Observer) {
	log.Printf("Deregistering observer %s\n", o.GetName())
	_, exists := observers[o.GetName()]
	if exists {
		delete(observers, o.GetName())
	} else {
		log.Printf("Cannot deregister. EventConsumer does not exists")
	}
}

func (r *RoutingListener) UpdateAll(event *Event) {
	for _, o := range observers {
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
