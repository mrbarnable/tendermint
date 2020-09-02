package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/light"
	lightprovider "github.com/tendermint/tendermint/light/provider"
	lighthttp "github.com/tendermint/tendermint/light/provider/http"
	lightdb "github.com/tendermint/tendermint/light/store/db"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	dbm "github.com/tendermint/tm-db"
)

var (
	chainID = "chain-1AKYw9"
	height  = 30
	hash    = "44307D0070881D6C06FE6E265AB396793FB15707BEA0A3809460EF5CBDCDA2DA"
)

func mustHexDecode(s string) []byte {
	bz, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return bz
}

func main() {
	err := run()
	if err != nil {
		panic(err)
	}
}

func run() error {
	servers := []string{
		"127.0.0.1:26657",
		"127.0.0.1:26660",
	}
	providers := make([]lightprovider.Provider, 0, len(servers))
	providerRemotes := make(map[lightprovider.Provider]string)
	for _, server := range servers {
		client, err := rpcClient(server)
		if err != nil {
			return fmt.Errorf("failed to set up RPC client: %w", err)
		}
		provider := lighthttp.NewWithClient(chainID, client)
		providers = append(providers, provider)
		// We store the RPC addresses keyed by provider, so we can find the address of the primary
		// provider used by the light client and use it to fetch consensus parameters.
		providerRemotes[provider] = server
	}
	trustOptions := light.TrustOptions{
		Period: 24 * time.Hour,
		Height: int64(height),
		Hash:   mustHexDecode(hash),
	}
	logger := log.NewTMLogger(os.Stdout)

	lc, err := light.NewClient(chainID, trustOptions, providers[0], providers[1:],
		lightdb.New(dbm.NewMemDB(), ""), light.Logger(logger), light.MaxRetryAttempts(5))
	if err != nil {
		return err
	}

	getVals(lc, 10)
	getVals(lc, 11)
	getVals(lc, 12)

	return nil
}

func getVals(lc *light.Client, height int64) {
	block, err := lc.VerifyLightBlockAtHeight(height, time.Now())
	if err != nil {
		panic(err)
	}
	fmt.Printf("vals at %v: %v\n", height, block.ValidatorSet)
}

// rpcClient sets up a new RPC client
func rpcClient(server string) (*rpchttp.HTTP, error) {
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	c, err := rpchttp.New(server, "/websocket")
	if err != nil {
		return nil, err
	}
	return c, nil
}
