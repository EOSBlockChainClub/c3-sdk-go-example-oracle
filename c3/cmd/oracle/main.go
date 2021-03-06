package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"

	"github.com/c3systems/c3-sdk-go-example-oracle/c3/cfg"
	"github.com/c3systems/c3-sdk-go-example-oracle/c3/pkg/coder"
	"github.com/c3systems/c3-sdk-go-example-oracle/c3/pkg/ethereumclient"
	"github.com/c3systems/c3-go/common/c3crypto"
	"github.com/c3systems/c3-go/common/txparamcoder"
	"github.com/c3systems/c3-go/core/chain/mainchain"
	"github.com/c3systems/c3-go/core/chain/statechain"
	"github.com/c3systems/c3-go/core/p2p/protobuff"
	methodTypes "github.com/c3systems/c3-go/core/types/methods"
	nodetypes "github.com/c3systems/c3-go/node/types"
	"github.com/davecgh/go-spew/spew"
	ipfsaddr "github.com/ipfs/go-ipfs-addr"
	csms "github.com/libp2p/go-conn-security-multistream"
	lCrypt "github.com/libp2p/go-libp2p-crypto"
	host "github.com/libp2p/go-libp2p-host"
	peer "github.com/libp2p/go-libp2p-peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	"github.com/libp2p/go-libp2p-peerstore/pstoremem"
	secio "github.com/libp2p/go-libp2p-secio"
	swarm "github.com/libp2p/go-libp2p-swarm"
	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	tcp "github.com/libp2p/go-tcp-transport"
	ma "github.com/multiformats/go-multiaddr"
	msmux "github.com/whyrusleeping/go-smux-multistream"
	yamux "github.com/whyrusleeping/go-smux-yamux"
)

var (
	pBuff   *protobuff.Node
	priv    *ecdsa.PrivateKey
	pub     *ecdsa.PublicKey
	pubAddr string
	newNode host.Host
	peerID  peer.ID
	vars    cfg.Vars
)

func getHeadblock() (mainchain.Block, error) {
	return mainchain.Block{}, nil
}

func broadcastTx(tx *statechain.Transaction) (*nodetypes.SendTxResponse, error) {
	return nil, nil
}

func main() {
	// 1. run the cli
	constants, err := cfg.New(os.Args)
	if err != nil {
		log.Fatalf("Error grabbing cfg: %v", err)
	}
	vars = constants.Get()

	if vars.ETH_NodeURL == "" {
		log.Fatalln("ethereum node URL is not set")
	}

	// 2. build the eth client
	ch := make(chan interface{})
	ethClient, err := ethereumclient.NewClient(&ethereumclient.Config{
		NodeURL:         vars.ETH_NodeURL,
		PrivateKey:      vars.ETH_PrivateKey,
		ContractAddress: vars.ETH_ContractAddress,
		ListenChan:      ch,
	})
	if err != nil {
		log.Fatalf("err building the eth client\n%v", err)
	}

	go ethClient.Listen()
	go func() {
		for {
			switch v := <-ch; v.(type) {
			case error:
				log.Printf("err on the eth client\n%v", err)

			case *ethereumclient.LogBuy:
				log.Println("log buy event")
				// note: don't need ok here bc of above switch statement
				l, _ := v.(*ethereumclient.LogBuy)
				go ethLogBuyHandler(ethClient, l)

			case *ethereumclient.LogDeposit:
				log.Println("log deposit event")
				// note: don't need ok here bc of above switch statement
				l, _ := v.(*ethereumclient.LogDeposit)
				go ethLogDepositHandler(ethClient, l)

			case *ethereumclient.LogWithdrawal:
				log.Println("log withdrawal event")
				// note: don't need ok here bc of above switch statement
				l, _ := v.(*ethereumclient.LogWithdrawal)
				go ethLogWithdrawalHandler(ethClient, l)

			default:
				log.Printf("received a message of an unknown type %T on the eth channel\n%v", v, v)

			}
		}
	}()

	// 3. build the c3 p2p node
	if err := buildNode(vars.C3_NodeURL); err != nil {
		log.Fatalf("err building node\n%v", err)
	}

	if vars.Genesis {
		log.Println("sending genesis block")
		if err := sendGenesisBlock(); err != nil {
			log.Fatalf("err sending genesis block\n%v", err)
		}
	}

	// 4. wait
	select {}
}

func ethLogBuyHandler(ethClient *ethereumclient.Client, l *ethereumclient.LogBuy) {
	log.Println("in buy handler")
	b, err := coder.EncodeETHLogBuy(l)
	if err != nil {
		log.Printf("err decoding\n%v", err)
		return
	}

	log.Println("building payload")
	payload := txparamcoder.ToJSONArray(
		txparamcoder.EncodeMethodName("processETHBuy"),
		txparamcoder.EncodeParam(hex.EncodeToString(b)),
	)

	log.Println("building tx")
	tx := statechain.NewTransaction(&statechain.TransactionProps{
		ImageHash: vars.ImageHash,
		Method:    methodTypes.InvokeMethod,
		Payload:   payload,
		From:      pubAddr,
	})
	log.Println("setting sig")
	if err = tx.SetSig(priv); err != nil {
		log.Printf("error setting sig\n%v", err)
		return
	}
	log.Println("setting tx hash")
	if err = tx.SetHash(); err != nil {
		log.Printf("error setting hash\n%v", err)
		return
	}
	log.Println("checking tx hash")
	if tx.Props().TxHash == nil {
		log.Println("tx hash is nil!")
		return
	}

	log.Println("serializing")
	txBytes, err := tx.Serialize()
	if err != nil {
		log.Printf("error getting tx bytes\n%v", err)
		return
	}

	log.Printf("tx hash\n%v", *tx.Props().TxHash)
	ch := make(chan interface{})
	if err := pBuff.ProcessTransaction.SendTransaction(peerID, txBytes, ch); err != nil {
		log.Printf("err processing tx\n%v", err)
		return
	}

	res := <-ch
	log.Printf("received response on channel %v", res)
}
func ethLogDepositHandler(ethClient *ethereumclient.Client, l *ethereumclient.LogDeposit) {
	log.Println("in dep handler")
	b, err := coder.EncodeETHLogDeposit(l)
	if err != nil {
		log.Printf("err decoding\n%v", err)
		return
	}

	payload := txparamcoder.ToJSONArray(
		txparamcoder.EncodeMethodName("processETHDeposit"),
		txparamcoder.EncodeParam(hex.EncodeToString(b)),
	)

	tx := statechain.NewTransaction(&statechain.TransactionProps{
		ImageHash: vars.ImageHash,
		Method:    methodTypes.InvokeMethod,
		Payload:   payload,
		From:      pubAddr,
	})
	if err = tx.SetSig(priv); err != nil {
		log.Printf("error setting sig\n%v", err)
		return
	}
	if err = tx.SetHash(); err != nil {
		log.Printf("error setting hash\n%v", err)
		return
	}
	if tx.Props().TxHash == nil {
		log.Println("tx hash is nil!")
		return
	}

	txBytes, err := tx.Serialize()
	if err != nil {
		log.Printf("error getting tx bytes\n%v", err)
		return
	}

	log.Printf("tx hash\n%v", *tx.Props().TxHash)
	ch := make(chan interface{})
	if err := pBuff.ProcessTransaction.SendTransaction(peerID, txBytes, ch); err != nil {
		log.Printf("err processing tx\n%v", err)
		return
	}

	res := <-ch
	log.Printf("received response on channel %v", res)
}
func ethLogWithdrawalHandler(ethClient *ethereumclient.Client, l *ethereumclient.LogWithdrawal) {
	log.Println("in withdrawal handler")
	b, err := coder.EncodeETHLogWithdrawal(l)
	if err != nil {
		log.Printf("err decoding\n%v", err)
		return
	}

	payload := txparamcoder.ToJSONArray(
		txparamcoder.EncodeMethodName("processETHWithdrawal"),
		txparamcoder.EncodeParam(hex.EncodeToString(b)),
	)

	tx := statechain.NewTransaction(&statechain.TransactionProps{
		ImageHash: vars.ImageHash,
		Method:    methodTypes.InvokeMethod,
		Payload:   payload,
		From:      pubAddr,
	})
	if err = tx.SetSig(priv); err != nil {
		log.Printf("error setting sig\n%v", err)
		return
	}
	if err = tx.SetHash(); err != nil {
		log.Printf("error setting hash\n%v", err)
		return
	}
	if tx.Props().TxHash == nil {
		log.Println("tx hash is nil!")
		return
	}

	txBytes, err := tx.Serialize()
	if err != nil {
		log.Printf("error getting tx bytes\n%v", err)
		return
	}

	log.Printf("tx hash\n%v", *tx.Props().TxHash)
	ch := make(chan interface{})
	if err := pBuff.ProcessTransaction.SendTransaction(peerID, txBytes, ch); err != nil {
		log.Printf("err processing tx\n%v", err)
		return
	}

	res := <-ch
	log.Printf("received response on channel %v", res)
}

// note: https://github.com/libp2p/go-libp2p-swarm/blob/da01184afe4c67bec58c5e73f3350ad80b624c0d/testing/testing.go#L39
func genUpgrader(n *swarm.Swarm) *tptu.Upgrader {
	id := n.LocalPeer()
	pk := n.Peerstore().PrivKey(id)
	secMuxer := new(csms.SSMuxer)
	secMuxer.AddTransport(secio.ID, &secio.Transport{
		LocalID:    id,
		PrivateKey: pk,
	})

	stMuxer := msmux.NewBlankTransport()
	stMuxer.AddTransport("/yamux/1.0.0", yamux.DefaultTransport)

	return &tptu.Upgrader{
		Secure:  secMuxer,
		Muxer:   stMuxer,
		Filters: n.Filters,
	}
}

func sendGenesisBlock() error {
	ch := make(chan interface{})
	tx := statechain.NewTransaction(&statechain.TransactionProps{
		ImageHash: vars.ImageHash,
		Method:    methodTypes.Deploy,
		Payload:   nil,
		From:      pubAddr,
	})

	if err := tx.SetHash(); err != nil {
		return err
	}

	if err := tx.SetSig(priv); err != nil {
		return err
	}

	txBytes, err := tx.Serialize()
	if err != nil {
		return err
	}

	if err := pBuff.ProcessTransaction.SendTransaction(peerID, txBytes, ch); err != nil {
		return err
	}

	v := <-ch

	switch v.(type) {
	case error:
		err, _ := v.(error)
		return err

	default:
		spew.Dump(v)

		return nil
	}
}

func buildNode(peerStr string) error {
	wPriv, wPub, err := lCrypt.GenerateKeyPairWithReader(lCrypt.RSA, 4096, rand.Reader)
	if err != nil {
		log.Printf("err generating keypairs %v", err)
		return err
	}

	pid, err := peer.IDFromPublicKey(wPub)
	if err != nil {
		log.Printf("err getting pid %v", err)
		return err
	}

	uri := "/ip4/0.0.0.0/tcp/9008"
	listen, err := ma.NewMultiaddr(uri)
	if err != nil {
		log.Printf("err listening %v", err)
		return err
	}

	ps := pstoremem.NewPeerstore()
	if err = ps.AddPrivKey(pid, wPriv); err != nil {
		log.Printf("err adding priv key %v", err)
		return err
	}
	if err = ps.AddPubKey(pid, wPub); err != nil {
		log.Printf("err adding pub key %v", err)
		return err
	}

	swarmNet := swarm.NewSwarm(context.Background(), pid, ps, nil)
	tcpTransport := tcp.NewTCPTransport(genUpgrader(swarmNet))
	if err = swarmNet.AddTransport(tcpTransport); err != nil {
		log.Printf("err adding transport %v", err)
		return err
	}
	if err = swarmNet.AddListenAddr(listen); err != nil {
		log.Printf("err adding listenaddr %v", err)
		return err
	}
	newNode = bhost.New(swarmNet)

	addr, err := ipfsaddr.ParseString(peerStr)
	if err != nil {
		log.Printf("err parsing peer string %v", err)
		return err
	}

	pinfo, err := peerstore.InfoFromP2pAddr(addr.Multiaddr())
	if err != nil {
		log.Printf("err getting pinfo %v", err)
		return err
	}

	log.Println("[node] FULL", addr.String())
	log.Println("[node] PIN INFO", pinfo)

	if err = newNode.Connect(context.Background(), *pinfo); err != nil {
		log.Printf("err connecting to peer; %v\n", err)
		return err
	}

	peerID = pinfo.ID
	newNode.Peerstore().AddAddrs(pinfo.ID, pinfo.Addrs, peerstore.PermanentAddrTTL)

	pBuff, err = protobuff.NewNode(&protobuff.Props{
		Host:                   newNode,
		GetHeadBlockFN:         getHeadblock,
		BroadcastTransactionFN: broadcastTx,
	})
	if err != nil {
		log.Printf("error starting protobuff node\n%v", err)
		return err
	}

	priv, pub, err = c3crypto.NewKeyPair()
	if err != nil {
		log.Printf("error getting keypair\n%v", err)
		return err
	}

	pubAddr, err = c3crypto.EncodeAddress(pub)
	if err != nil {
		log.Printf("error getting addr\n%v", err)
		return err
	}

	log.Println("pub addr\n", pubAddr)

	return nil
}
