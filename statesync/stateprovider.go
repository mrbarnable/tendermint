package statesync

import (
	"fmt"
	"strings"
	"time"

	dbm "github.com/tendermint/tm-db"

	"github.com/tendermint/tendermint/libs/log"
	tmsync "github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/light"
	lightprovider "github.com/tendermint/tendermint/light/provider"
	lighthttp "github.com/tendermint/tendermint/light/provider/http"
	lightrpc "github.com/tendermint/tendermint/light/rpc"
	lightdb "github.com/tendermint/tendermint/light/store/db"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

//go:generate mockery --case underscore --name StateProvider

// StateProvider is a provider of trusted state data for bootstrapping a node. This refers
// to the state.State object, not the state machine.
type StateProvider interface {
	// AppHash returns the app hash after the given height has been committed.
	AppHash(height uint64) ([]byte, error)
	// Commit returns the commit at the given height.
	Commit(height uint64) (*types.Commit, error)
	// State returns a state object at the given height.
	State(height uint64) (sm.State, error)
}

// lightClientStateProvider is a state provider using the light client.
type lightClientStateProvider struct {
	tmsync.Mutex  // light.Client is not concurrency-safe
	lc            *light.Client
	version       tmstate.Version
	initialHeight int64
	providers     map[lightprovider.Provider]string
}

// NewLightClientStateProvider creates a new StateProvider using a light client and RPC clients.
func NewLightClientStateProvider(
	chainID string,
	version tmstate.Version,
	initialHeight int64,
	servers []string,
	trustOptions light.TrustOptions,
	logger log.Logger,
) (StateProvider, error) {
	if len(servers) < 2 {
		return nil, fmt.Errorf("at least 2 RPC servers are required, got %v", len(servers))
	}

	providers := make([]lightprovider.Provider, 0, len(servers))
	providerRemotes := make(map[lightprovider.Provider]string)
	for _, server := range servers {
		client, err := rpcClient(server)
		if err != nil {
			return nil, fmt.Errorf("failed to set up RPC client: %w", err)
		}
		provider := lighthttp.NewWithClient(chainID, client)
		providers = append(providers, provider)
		// We store the RPC addresses keyed by provider, so we can find the address of the primary
		// provider used by the light client and use it to fetch consensus parameters.
		providerRemotes[provider] = server
	}

	lc, err := light.NewClient(chainID, trustOptions, providers[0], providers[1:],
		lightdb.New(dbm.NewMemDB(), ""), light.Logger(logger), light.MaxRetryAttempts(5))
	if err != nil {
		return nil, err
	}
	return &lightClientStateProvider{
		lc:            lc,
		version:       version,
		initialHeight: initialHeight,
		providers:     providerRemotes,
	}, nil
}

// AppHash implements StateProvider.
func (s *lightClientStateProvider) AppHash(height uint64) ([]byte, error) {
	s.Lock()
	defer s.Unlock()

	// We have to fetch the next height, which contains the app hash for the previous height.
	header, err := s.lc.VerifyLightBlockAtHeight(int64(height+1), time.Now())
	if err != nil {
		return nil, err
	}
	return header.AppHash, nil
}

// Commit implements StateProvider.
func (s *lightClientStateProvider) Commit(height uint64) (*types.Commit, error) {
	s.Lock()
	defer s.Unlock()
	header, err := s.lc.VerifyLightBlockAtHeight(int64(height), time.Now())
	if err != nil {
		return nil, err
	}
	return header.Commit, nil
}

// State implements StateProvider.
func (s *lightClientStateProvider) State(height uint64) (sm.State, error) {
	s.Lock()
	defer s.Unlock()

	state := sm.State{
		ChainID:       s.lc.ChainID(),
		Version:       s.version,
		InitialHeight: s.initialHeight,
	}
	if state.InitialHeight == 0 {
		state.InitialHeight = 1
	}

	// We need to verify the previous block to get the validator set.
	/*prevLightBlock, err := s.lc.VerifyLightBlockAtHeight(int64(height-1), time.Now())
	if err != nil {
		return sm.State{}, err
	}*/
	lightBlock, err := s.lc.VerifyLightBlockAtHeight(int64(height), time.Now())
	if err != nil {
		return sm.State{}, err
	}
	nextLightBlock, err := s.lc.VerifyLightBlockAtHeight(int64(height+1), time.Now())
	if err != nil {
		return sm.State{}, err
	}

	nextNextLightBlock, err := s.lc.VerifyLightBlockAtHeight(int64(height+2), time.Now())
	if err != nil {
		return sm.State{}, err
	}

	state.LastBlockHeight = lightBlock.Height
	state.LastBlockTime = lightBlock.Time
	state.LastBlockID = lightBlock.Commit.BlockID
	state.AppHash = nextLightBlock.AppHash
	state.LastResultsHash = nextLightBlock.LastResultsHash
	state.LastValidators = lightBlock.ValidatorSet
	state.Validators = nextLightBlock.ValidatorSet
	state.NextValidators = nextNextLightBlock.ValidatorSet
	state.LastHeightValidatorsChanged = int64(height)

	fmt.Printf("statesync-state: %v\n", state)

	// We'll also need to fetch consensus params via RPC, using light client verification.
	primaryURL, ok := s.providers[s.lc.Primary()]
	if !ok || primaryURL == "" {
		return sm.State{}, fmt.Errorf("could not find address for primary light client provider")
	}
	primaryRPC, err := rpcClient(primaryURL)
	if err != nil {
		return sm.State{}, fmt.Errorf("unable to create RPC client: %w", err)
	}
	rpcclient := lightrpc.NewClient(primaryRPC, s.lc)
	result, err := rpcclient.ConsensusParams(&nextLightBlock.Height)
	if err != nil {
		return sm.State{}, fmt.Errorf("unable to fetch consensus parameters for height %v: %w",
			nextLightBlock.Height, err)
	}
	state.ConsensusParams = result.ConsensusParams

	return state, nil
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
