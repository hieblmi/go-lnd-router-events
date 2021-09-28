# ⚡go-lnd-router-events
This module allows subscribers to get notified about [HTLC](https://rusty.ozlabs.org/?p=462) [SettleEvent](https://api.lightning.community/#routerrpc-settleevent)s and retrieve
the following statistics from its related preceeding [ForwardEvent](https://api.lightning.community/#routerrpc-forwardevent):
```
* FromPubKey   
* FromAlias    
* IncomingMSats
* ToAlias      
* ToPubKey     
* OutgoingMSats
* ChanId_In    
* ChanId_Out   
* HtlcId_In    
* HtlcId_Out   
```

## Installation
```
go get github.com/hieblmi/go-lnd-router-events
```

## How it works
LND's router RPC interface allows to listen to [ForwardEvent](https://api.lightning.community/#routerrpc-forwardevent)s that basically represent an attempt from an initiating peer on a route to push a payment through our node. The successful forward events can be obtained by clients who implement
```
type Observer interface {   
    Update(event *Event)
    GetName() string    
}
```
and register their observer like so:
```
listener := events.New(&events.Config{
         MacaroonPath: config.MacaroonPath,
         CertPath:     config.CertPath,
         RpcHost:      config.RpcHost,
 })
 listener.Register(&LndEventObserver{
         Name:     "YourObserverName",                 
 })
 listener.Start()
 ```
 The update method then receives successful forward events:
 ```
 func (o *yourObserver) Update(e *events.Event) {
  fmt.Println(e.Type)
  fmt.Println(e.IncomingMSats)
  fmt.Println(e.OutgoingMSats)
 }
```

### Little more detail
 To retrieve the details of a successful payment we have to match the SettleEvent's ids to the ids of the preceeding ForwardEvent. In this instance this is solved by keeping a map of [key|ForwardEvent] and retrieve a given forward attempt when the respective SettleEvent occurs. The key is calculated by 
```
IncomingChannelId + OutgoingChannelId + IncomingHtlcId + OutgoingHtlcId
```
In case the payment through our node isn't successful we will observe a [ForwardFailEvent](https://api.lightning.community/#routerrpc-forwardfailevent) or [LinkFailEvent](https://api.lightning.community/#routerrpc-linkfailevent). In these cases we simply remove the respective ForwardEvent from our map.

### Disclaimer
This stuff is experimental so do use it with care. I am highly appreciative of feedback and ways to improve my understanding of everything Lightning.

If you like what you see you could send me a tip :-)

Download a [LightningAddress](https://lightningaddress.com/) enabled wallet like [Blixt](https://blixtwallet.github.io/) or [BlueWallet](https://bluewallet.io/) and tip to: ⚡heebs@allmysats.com
