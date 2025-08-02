package mpt

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/triedb"
)

var _ vm.StateDB = (*StateDB)(nil)

type StateDB struct {
	*state.StateDB
}

func NewKeeper(dbPath, namespace, ancient string, readonly bool, parentRoot common.Hash) (*StateDB, error) {
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

	return &StateDB{
		StateDB: statedb,
	}, nil
}
