package mpt

import (
	"math/big"

	sdk "github.com/cosmos/cosmos-sdk/types"
	cosmosevmtypes "github.com/cosmos/evm/types"
	"github.com/cosmos/evm/x/vm/statedb"
	"github.com/cosmos/evm/x/vm/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/triedb"
)

var _ vm.StateDB = (*Keeper)(nil)

type Keeper struct {
	*state.StateDB
}

func NewKeeper(dbPath, namespace, ancient string, readonly bool, parentRoot common.Hash) (*Keeper, error) {
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
	statedb, err := state.New(parentRoot, cachingdb)
	if err != nil {
		return nil, err
	}

	return &Keeper{
		StateDB: statedb,
	}, nil
}

func (k *Keeper) NewEVM(ctx sdk.Context, msg core.Message, cfg *statedb.EVMConfig, tracer *tracing.Hooks) *vm.EVM {
	ctx = k.SetConsensusParamsInCtx(ctx)
	blockCtx := vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     k.GetHashFn(ctx),
		Coinbase:    cfg.CoinBase,
		GasLimit:    cosmosevmtypes.BlockGasLimit(ctx),
		BlockNumber: big.NewInt(ctx.BlockHeight()),
		Time:        uint64(ctx.BlockHeader().Time.Unix()), //#nosec G115 -- int overflow is not a concern here
		Difficulty:  big.NewInt(0),                         // unused. Only required in PoW context
		BaseFee:     cfg.BaseFee,
		Random:      &common.MaxHash, // need to be different than nil to signal it is after the merge and pick up the right opcodes
	}

	ethCfg := types.GetEthChainConfig()
	txCtx := core.NewEVMTxContext(&msg)
	if tracer == nil {
		tracer = k.Tracer(ctx, msg, ethCfg)
	}
	vmConfig := k.VMConfig(ctx, msg, cfg, tracer)

	signer := msg.From
	accessControl := types.NewRestrictedPermissionPolicy(&cfg.Params.AccessControl, signer)

	// Set hooks for the EVM opcodes
	evmHooks := types.NewDefaultOpCodesHooks()
	evmHooks.AddCreateHooks(
		accessControl.GetCreateHook(signer),
	)
	evmHooks.AddCallHooks(
		accessControl.GetCallHook(signer),
		k.GetPrecompilesCallHook(ctx),
	)
	return vm.NewEVMWithHooks(evmHooks, blockCtx, txCtx, k.StateDB, ethCfg, vmConfig)
}
