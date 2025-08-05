package mpt

import (
	"math/big"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/evm/x/vm/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
)

// TxConfig encapulates the readonly information of current tx for `StateDB`.
type TxConfig struct {
	CurrentRoot common.Hash
	BlockHash   common.Hash // hash of current block
	BlockNumber uint64
	TxHash      common.Hash // hash of current tx
	TxIndex     uint        // the index of current transaction
	LogIndex    uint        // the index of next log within current block
}

// NewTxConfig returns a TxConfig
func NewTxConfig(bhash, thash common.Hash, txIndex, logIndex uint) TxConfig {
	return TxConfig{
		BlockHash: bhash,
		TxHash:    thash,
		TxIndex:   txIndex,
		LogIndex:  logIndex,
	}
}

// NewEmptyTxConfig construct an empty TxConfig,
// used in context where there's no transaction, e.g. `eth_call`/`eth_estimateGas`.
func NewEmptyTxConfig(bhash common.Hash) TxConfig {
	return TxConfig{
		BlockHash: bhash,
		TxHash:    common.Hash{},
		TxIndex:   0,
		LogIndex:  0,
	}
}

// EVMConfig encapsulates common parameters needed to create an EVM to execute a message
// It's mainly to reduce the number of method parameters
type EVMConfig struct {
	Params                  types.Params
	CoinBase                common.Address
	BaseFee                 *big.Int
	EnablePreimageRecording bool
}

// EVMConfig creates the EVMConfig based on current state
func (k *Keeper) EVMConfig(ctx sdk.Context, proposerAddress sdk.ConsAddress) (*EVMConfig, error) {
	params := k.GetParams(ctx)

	// get the coinbase address from the block proposer
	coinbase, err := k.GetCoinbaseAddress(ctx, proposerAddress)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to obtain coinbase address")
	}

	baseFee := k.GetBaseFee(ctx)
	return &EVMConfig{
		Params:   params,
		CoinBase: coinbase,
		BaseFee:  baseFee,
	}, nil
}

// TxConfig loads `TxConfig` from current transient storage
func (k *Keeper) TxConfig(ctx sdk.Context, txHash common.Hash) TxConfig {
	return NewTxConfig(
		common.BytesToHash(ctx.HeaderHash()), // BlockHash
		txHash,                               // TxHash
		uint(k.GetTxIndexTransient(ctx)),     // TxIndex
		uint(k.GetLogSizeTransient(ctx)),     // LogIndex
	)
}

// VMConfig creates an EVM configuration from the debug setting and the extra EIPs enabled on the
// module parameters. The config generated uses the default JumpTable from the EVM.
func (k Keeper) VMConfig(ctx sdk.Context, _ core.Message, cfg *EVMConfig, tracer *tracing.Hooks) vm.Config {
	noBaseFee := true
	// if types.IsLondon(types.GetEthChainConfig(), ctx.BlockHeight()) {
	// noBaseFee = k.feeMarketWrapper.GetParams(ctx).NoBaseFee
	// }

	return vm.Config{
		EnablePreimageRecording: cfg.EnablePreimageRecording,
		Tracer:                  tracer,
		NoBaseFee:               noBaseFee,
		ExtraEips:               cfg.Params.EIPs(),
	}
}
