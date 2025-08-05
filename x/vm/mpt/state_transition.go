package mpt

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"

	cmttypes "github.com/cometbft/cometbft/types"

	cosmosevmtypes "github.com/cosmos/evm/types"
	"github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
)

// NewEVM generates a go-ethereum VM from the provided Message fields and the chain parameters
// (ChainConfig and module Params). It additionally sets the validator operator address as the
// coinbase address to make it available for the COINBASE opcode, even though there is no
// beneficiary of the coinbase transaction (since we're not mining).
//
// NOTE: the RANDOM opcode is currently not supported since it requires
// RANDAO implementation. See https://github.com/evmos/ethermint/pull/1520#pullrequestreview-1200504697
// for more information.
func (k *Keeper) NewEVM(
	ctx sdk.Context,
	msg core.Message,
	cfg *EVMConfig,
	tracer *tracing.Hooks,
	stateDB vm.StateDB,
) *vm.EVM {
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
	return vm.NewEVMWithHooks(evmHooks, blockCtx, txCtx, stateDB, ethCfg, vmConfig)
}

// GetHashFn implements vm.GetHashFunc for Ethermint. It handles 3 cases:
//  1. The requested height matches the current height from context (and thus same epoch number)
//  2. The requested height is from an previous height from the same chain epoch
//  3. The requested height is from a height greater than the latest one
func (k Keeper) GetHashFn(ctx sdk.Context) vm.GetHashFunc {
	return func(height uint64) common.Hash {
		h, err := cosmosevmtypes.SafeInt64(height)
		if err != nil {
			k.Logger(ctx).Error("failed to cast height to int64", "error", err)
			return common.Hash{}
		}

		switch {
		case ctx.BlockHeight() == h:
			// Case 1: The requested height matches the one from the context so we can retrieve the header
			// hash directly from the context.
			// Note: The headerHash is only set at begin block, it will be nil in case of a query context
			headerHash := ctx.HeaderHash()
			if len(headerHash) != 0 {
				return common.BytesToHash(headerHash)
			}

			// only recompute the hash if not set (eg: checkTxState)
			contextBlockHeader := ctx.BlockHeader()
			header, err := cmttypes.HeaderFromProto(&contextBlockHeader)
			if err != nil {
				k.Logger(ctx).Error("failed to cast tendermint header from proto", "error", err)
				return common.Hash{}
			}

			headerHash = header.Hash()
			return common.BytesToHash(headerHash)

		case ctx.BlockHeight() > h:
			// Case 2: if the chain is not the current height we need to retrieve the hash from the store for the
			// current chain epoch. This only applies if the current height is greater than the requested height.
			histInfo, err := k.stakingKeeper.GetHistoricalInfo(ctx, h)
			if err != nil {
				k.Logger(ctx).Debug("error while getting historical info", "height", h, "error", err.Error())
				return common.Hash{}
			}

			header, err := cmttypes.HeaderFromProto(&histInfo.Header)
			if err != nil {
				k.Logger(ctx).Error("failed to cast tendermint header from proto", "error", err)
				return common.Hash{}
			}

			return common.BytesToHash(header.Hash())
		default:
			// Case 3: heights greater than the current one returns an empty hash.
			return common.Hash{}
		}
	}
}

// ApplyTransaction runs and attempts to perform a state transition with the given transaction (i.e Message), that will
// only be persisted (committed) to the underlying KVStore if the transaction does not fail.
//
// # Gas tracking
//
// Ethereum consumes gas according to the EVM opcodes instead of general reads and writes to store. Because of this, the
// state transition needs to ignore the SDK gas consumption mechanism defined by the GasKVStore and instead consume the
// amount of gas used by the VM execution. The amount of gas used is tracked by the EVM and returned in the execution
// result.
//
// Prior to the execution, the starting tx gas meter is saved and replaced with an infinite gas meter in a new context
// to ignore the SDK gas consumption config values (read, write, has, delete).
// After the execution, the gas used from the message execution will be added to the starting gas consumed, taking into
// consideration the amount of gas returned. Finally, the context is updated with the EVM gas consumed value prior to
// returning.
//
// For relevant discussion see: https://github.com/cosmos/cosmos-sdk/discussions/9072
func (k *Keeper) ApplyTransaction(ctx sdk.Context, tx *ethtypes.Transaction) (*types.MsgEthereumTxResponse, error) {
	cfg, err := k.EVMConfig(ctx, ctx.BlockHeader().ProposerAddress)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to load evm config")
	}
	txConfig := k.TxConfig(ctx, tx.Hash())

	// get the signer according to the chain rules from the config and block height
	signer := ethtypes.MakeSigner(types.GetEthChainConfig(), big.NewInt(ctx.BlockHeight()), uint64(ctx.BlockTime().Unix())) //#nosec G115 -- int overflow is not a concern here
	msg, err := core.TransactionToMessage(tx, signer, cfg.BaseFee)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to return ethereum transaction as core message")
	}

	stateDB, err := k.NewStateDB(txConfig.CurrentRoot)
	if err != nil {
		return nil, err
	}
	evm := k.NewEVM(ctx, *msg, cfg, nil, stateDB)

	// create a cache context to revert state. The cache context is only committed when both tx and hooks executed successfully.
	// Didn't use `Snapshot` because the context stack has exponential complexity on certain operations,
	// thus restricted to be used only inside `ApplyMessage`.
	tmpCtx, commitFn := ctx.CacheContext()

	// pass true to commit the StateDB
	result, err := k.ApplyMessageWithConfig(tmpCtx, evm, *msg, nil, cfg, txConfig)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to apply ethereum core message")
	}
	root := stateDB.IntermediateRoot(false)

	if !result.Failed() {
		receipt := core.MakeReceipt(evm, result, stateDB, big.NewInt(int64(txConfig.BlockNumber)), txConfig.BlockHash, tx, result.GasUsed, root.Bytes())

		signerAddr, err := signer.Sender(tx)
		if err != nil {
			return nil, errorsmod.Wrap(err, "failed to extract sender address from ethereum transaction")
		}

		eventsLen := len(tmpCtx.EventManager().Events())

		// Note: PostTxProcessing hooks currently do not charge for gas
		// and function similar to EndBlockers in abci, but for EVM transactions
		if err = k.PostTxProcessing(ctx, signerAddr, *msg, receipt); err != nil {
			// If hooks returns an error, revert the whole tx.
			k.Logger(ctx).Error("tx post processing failed", "error", err)
			// If the tx failed in post processing hooks, we should clear all log-related data
			// to match EVM behavior where transaction reverts clear all effects including logs
			receipt.Logs = nil
			receipt.Logs = nil
			receipt.Bloom = ethtypes.Bloom{} // Clear bloom filter
		} else if commitFn != nil {
			commitFn()

			// Since the post-processing can alter the log, we need to update the result
			result.Logs = types.NewLogsFromEth(receipt.Logs)
			events := tmpCtx.EventManager().Events()
			if len(events) > eventsLen {
				ctx.EventManager().EmitEvents(events[eventsLen:])
			}
		}
	}

	return result, nil
}

// ApplyMessage calls ApplyMessageWithConfig with an empty TxConfig.
func (k *Keeper) ApplyMessage(ctx sdk.Context, msg core.Message, tracer *tracing.Hooks) (*types.MsgEthereumTxResponse, error) {
	cfg, err := k.EVMConfig(ctx, ctx.BlockHeader().ProposerAddress)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to load evm config")
	}
	txConfig := NewEmptyTxConfig(common.BytesToHash(ctx.HeaderHash()))
	// TODO : how to calculate state db root?
	stateDB, err := k.NewStateDB(txConfig.CurrentRoot)
	if err != nil {
		return nil, err
	}
	evm := k.NewEVM(ctx, msg, cfg, nil, stateDB)

	return k.ApplyMessageWithConfig(ctx, evm, msg, tracer, cfg, txConfig)
}

// ApplyMessageWithConfig computes the new state by applying the given message against the existing state.
// If the message fails, the VM execution error with the reason will be returned to the client
// and the transaction won't be committed to the store.
func (k *Keeper) ApplyMessageWithConfig(ctx sdk.Context, evm *vm.EVM, msg core.Message, tracer *tracing.Hooks, cfg *EVMConfig, txConfig TxConfig) (*types.MsgEthereumTxResponse, error) {
	var (
		ret   []byte // return bytes from evm execution
		vmErr error  // vm errors do not effect consensus and are therefore not assigned to err

		gp          = new(core.GasPool).AddGas(ctx.GasMeter().Limit())
		leftoverGas = msg.GasLimit
	)

	stateDB := evm.StateDB.(*state.StateDB)

	// Allow the tracer captures the tx level events, mainly the gas consumption.
	vmCfg := evm.Config
	if vmCfg.Tracer != nil {
		vmCfg.Tracer.OnTxStart(
			evm.GetVMContext(),
			ethtypes.NewTx(&ethtypes.LegacyTx{To: msg.To, Data: msg.Data, Value: msg.Value, Gas: msg.GasLimit}),
			msg.From,
		)
		defer func() {
			if vmCfg.Tracer.OnTxEnd != nil {
				vmCfg.Tracer.OnTxEnd(&ethtypes.Receipt{GasUsed: msg.GasLimit - leftoverGas}, vmErr)
			}
		}()
	}

	result, err := core.ApplyMessage(evm, &msg, gp)
	if err != nil {
		return nil, err
	}
	vmErr = result.Err
	ret = result.Return()

	return &types.MsgEthereumTxResponse{
		GasUsed: result.UsedGas,
		VmError: vmErr.Error(),
		Ret:     ret,
		Logs:    types.NewLogsFromEth(stateDB.GetLogs(txConfig.TxHash, txConfig.BlockNumber, txConfig.BlockHash)),
		Hash:    txConfig.TxHash.Hex(),
	}, nil
}

// SetConsensusParamsInCtx will return the original context if consensus params already exist in it, otherwise, it will
// query the consensus params from the consensus params keeper and then set it in context.
func (k *Keeper) SetConsensusParamsInCtx(ctx sdk.Context) sdk.Context {
	cp := ctx.ConsensusParams()
	if cp.Block != nil {
		return ctx
	}

	res, err := k.consensusKeeper.Params(ctx, &consensustypes.QueryParamsRequest{})
	if err != nil {
		return ctx
	}
	return ctx.WithConsensusParams(*res.Params)
}
