package mpt

import (
	"math/big"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/evm/x/vm/types"
	"github.com/cosmos/evm/x/vm/wrappers"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

type Keeper struct {
	// Protobuf codec
	cdc codec.BinaryCodec
	// Store key required for the EVM Prefix KVStore. It is required by:
	// - storing account's Storage State
	// - storing account's Code
	// - storing transaction Logs
	// - storing Bloom filters by block height. Needed for the Web3 API.
	storeKey storetypes.StoreKey
	// key to access the transient store, which is reset on every block during Commit
	transientKey storetypes.StoreKey

	cdb *state.CachingDB

	// access historical headers for EVM state transition execution
	stakingKeeper types.StakingKeeper
	// fetch EIP1559 base fee and parameters
	feeMarketWrapper *wrappers.FeeMarketWrapper
	// consensusKeeper is used to get consensus params during query contexts.
	// This is needed as block.gasLimit is expected to be available in eth_call, which is routed through Cosmos SDK's
	// grpc query router. This query router builds a context WITHOUT consensus params, so we manually supply the context
	// with consensus params when not set in context.
	consensusKeeper types.ConsensusParamsKeeper
	// Tracer used to collect execution traces from the EVM transaction execution
	tracer string
	hooks  types.EvmHooks
}

func NewKeeper(
	dbPath, namespace, ancient string,
	readonly bool,
	sk types.StakingKeeper,
	fmk types.FeeMarketKeeper,
	consensusKeeper types.ConsensusParamsKeeper,
	erc20Keeper types.Erc20Keeper,
	tracer string,
) (*Keeper, error) {
	// open keyvalue database
	kvdb, err := pebble.New(dbPath, 0, 0, namespace, readonly, true)
	if err != nil {
		return nil, err
	}
	ethdb, err := rawdb.NewDatabaseWithFreezer(kvdb, ancient, namespace, readonly)
	if err != nil {
		return nil, err
	}
	triedb := triedb.NewDatabase(ethdb, triedb.HashDefaults)
	cachingdb := state.NewDatabase(triedb, nil)

	feeMarketWrapper := wrappers.NewFeeMarketWrapper(fmk)

	return &Keeper{
		cdb:              cachingdb,
		consensusKeeper:  consensusKeeper,
		stakingKeeper:    sk,
		feeMarketWrapper: feeMarketWrapper,
		tracer:           tracer,
	}, nil
}

func (k *Keeper) NewStateDB(root common.Hash) (*state.StateDB, error) {
	statedb, err := state.New(root, k.cdb)
	if err != nil {
		return nil, err
	}
	return statedb, nil
}

// Logger returns a module-specific logger.
func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", types.ModuleName)
}

func (k Keeper) Tracer(ctx sdk.Context, msg core.Message, ethCfg *params.ChainConfig) *tracing.Hooks {
	return types.NewTracer(k.tracer, msg, ethCfg, ctx.BlockHeight(), uint64(ctx.BlockTime().Unix())) //#nosec G115 -- int overflow is not a concern here
}

// GetPrecompilesCallHook returns a closure that can be used to instantiate the EVM with a specific
// precompile instance.
func (k *Keeper) GetPrecompilesCallHook(ctx sdktypes.Context) types.CallHook {
	return func(evm *vm.EVM, _ common.Address, recipient common.Address) error {
		// Check if the recipient is a precompile contract and if so, load the precompile instance
		precompile, found := evm.Precompile(recipient)

		var precompiles = make(map[common.Address]vm.PrecompiledContract)
		if found {
			precompiles[recipient] = precompile
		}

		// If the precompile instance is created, we have to update the EVM with
		// only the recipient precompile and add it's address to the access list.
		if found {
			evm.WithPrecompiles(precompiles)
			evm.StateDB.AddAddressToAccessList(recipient)
		}

		return nil
	}
}

// ----------------------------------------------------------------------------
// Tx
// ----------------------------------------------------------------------------

// SetTxIndexTransient set the index of processing transaction
func (k Keeper) SetTxIndexTransient(ctx sdk.Context, index uint64) {
	store := ctx.TransientStore(k.transientKey)
	store.Set(types.KeyPrefixTransientTxIndex, sdk.Uint64ToBigEndian(index))
}

// GetTxIndexTransient returns EVM transaction index on the current block.
func (k Keeper) GetTxIndexTransient(ctx sdk.Context) uint64 {
	store := ctx.TransientStore(k.transientKey)
	return sdk.BigEndianToUint64(store.Get(types.KeyPrefixTransientTxIndex))
}

// ----------------------------------------------------------------------------
// Hooks
// ----------------------------------------------------------------------------

// SetHooks sets the hooks for the EVM module
// Called only once during initialization, panics if called more than once.
func (k *Keeper) SetHooks(eh types.EvmHooks) *Keeper {
	if k.hooks != nil {
		panic("cannot set evm hooks twice")
	}

	k.hooks = eh
	return k
}

// PostTxProcessing delegates the call to the hooks.
// If no hook has been registered, this function returns with a `nil` error
func (k *Keeper) PostTxProcessing(
	ctx sdk.Context,
	sender common.Address,
	msg core.Message,
	receipt *ethtypes.Receipt,
) error {
	if k.hooks == nil {
		return nil
	}
	return k.hooks.PostTxProcessing(ctx, sender, msg, receipt)
}

// ----------------------------------------------------------------------------
// Log
// ----------------------------------------------------------------------------

// GetLogSizeTransient returns EVM log index on the current block.
func (k Keeper) GetLogSizeTransient(ctx sdk.Context) uint64 {
	store := ctx.TransientStore(k.transientKey)
	return sdk.BigEndianToUint64(store.Get(types.KeyPrefixTransientLogSize))
}

// SetLogSizeTransient fetches the current EVM log index from the transient store, increases its
// value by one and then sets the new index back to the transient store.
func (k Keeper) SetLogSizeTransient(ctx sdk.Context, logSize uint64) {
	store := ctx.TransientStore(k.transientKey)
	store.Set(types.KeyPrefixTransientLogSize, sdk.Uint64ToBigEndian(logSize))
}

// GetBaseFee returns current base fee, return values:
// - `nil`: london hardfork not enabled.
// - `0`: london hardfork enabled but feemarket is not enabled.
// - `n`: both london hardfork and feemarket are enabled.
func (k Keeper) GetBaseFee(ctx sdk.Context) *big.Int {
	ethCfg := types.GetEthChainConfig()
	if !types.IsLondon(ethCfg, ctx.BlockHeight()) {
		return nil
	}
	baseFee := k.feeMarketWrapper.GetBaseFee(ctx)
	if baseFee == nil {
		// return 0 if feemarket not enabled.
		baseFee = big.NewInt(0)
	}
	return baseFee
}
