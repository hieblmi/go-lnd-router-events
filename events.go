package router

import (
	"context"
	"encoding/json"
	"flag"
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
	Register(observer Observer)
	Deregister(observer Observer)
	Start()
	notifyAll(event Event)
}

type RoutingListener struct{}

type Observer interface {
	Update(event Event)
}

type EventConsumer struct {
	Name String
}

type Event struct {
	fromPubKey    string
	fromAlias     string
	incomingMSats uint64
	toAlias       string
	toPubKey      string
	outgoingMSats uint64
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
func New(cfg *Config) *RoutingListener {
	observers = make(map[string]Observer)
	c := flag.String("config", "./config.json", "Specify the configuration file")
	flag.Parse()
	file, err := os.Open(*c)
	if err != nil {
		log.Fatal("Cannot open config file: ", err)
	}
	defer file.Close()

	config := Config{}
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		log.Fatal("Cannot decode config JSON: ", err)
	}

	b, err := json.MarshalIndent(config, "", "      ")
	if err != nil {
		log.Println("Cannot indent json config.")
	}
	log.Printf("Printing config.json: %s\n", string(b))

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
	defer conn.Close()

	router = routerrpc.NewRouterClient(conn)
	lncli = lnrpc.NewLightningClient(conn)

	return &EventListener{}
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

		switch event.Event.(type) {
		//		case *routerrpc.HtlcEvent_SettleEvent:
		//			log.Printf("Settle Event Preimage: %#v\n", event.GetSettleEvent())
		case *routerrpc.HtlcEvent_ForwardEvent:
			incomingChanInfo, err := lnClient.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: event.IncomingChannelId})
			if err != nil {
				log.Println("Cannot get incoming channel info")
				continue
			}
			outgoingChanInfo, err := lnClient.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: event.OutgoingChannelId})
			if err != nil {
				log.Println("Cannot get outgoing channel info")
				continue
			}
			UpdateAll(&Event{
				fromPubKey:    incomingChanInfo.Node1Pub,
				fromAlias:     incomingChanInfo.Node1Pub,
				incomingMSats: event.GetForwardEvent().GetInfo().IncomingAmtMsat,
				toAlias:       outgoingChanInfo.Node2Pub,
				toPubKey:      outgoingChanInfo.Node2Pub,
				outgoingMSats: event.GetForwardEvent().GetInfo().OutgoingAmtMsat,
			})
			//	log.Printf("ChanInfo: %#v\n", incomingChanInfo)
			log.Printf("Forward Event incoming msats: %#v\n", event.GetIncomingChannelId())
			log.Printf("Forward Event incoming msats: %#v\n", event.GetOutgoingChannelId())
			log.Printf("Forward Event incoming msats: %d\n", event.GetForwardEvent().GetInfo().IncomingAmtMsat)
			log.Printf("Forward Event outgoing msats: %d\n", event.GetForwardEvent().GetInfo().OutgoingAmtMsat)
			log.Printf("Forward Event fee msats: %d\n", event.GetForwardEvent().GetInfo().IncomingAmtMsat-event.GetForwardEvent().GetInfo().OutgoingAmtMsat)
		}
	}
}

func (r *RoutingListener) Register(c EventConsumer) {
	log.Printf("Registering observer %s\n", c.Name)
	observers[c.name] = c
}

func (r *RoutingListener) Deregister(c EventConsumer) {
	log.Printf("Deregistering observer %s\n", c.Name)
	_, exists := observers[c.name]
	if exists {
		delete(observers, c.name)
	} else {
		log.Printf("Cannot deregister. EventConsumer does not exists")
	}
}

func (r *RoutingListener) UpdateAll(event *Event) {
	for _, eventConsumer := range observers {
		eventConsumer.Update(event)
	}
}
