// Copyright 2015 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package kv

import (
	"github.com/juju/errors"
	"github.com/ngaut/pool"
	"github.com/pingcap/tidb/util/errors2"
)

// conditionType is the type for condition consts.
type conditionType int

const (
	// conditionIfNotExist means the condition doesn't exist.
	conditionIfNotExist conditionType = iota + 1
	// conditionIfEqual means the condition is equals.
	conditionIfEqual
	// conditionForceSet means the condition is force set.
	conditionForceSet
)

var (
	p = pool.NewCache("memdb pool", 100, func() interface{} {
		return NewMemDbBuffer()
	})
)

// conditionValue is a data structure used to store current stored data and data verification condition.
type conditionValue struct {
	originValue []byte
	condition   conditionType
}

// IsErrNotFound checks if err is a kind of NotFound error.
func IsErrNotFound(err error) bool {
	if errors2.ErrorEqual(err, ErrNotExist) {
		return true
	}

	return false
}

// MemBuffer is the interface for transaction buffer of update in a transaction
type MemBuffer interface {
	// shares the same interface as the read-only snapshot
	// and it implies that MemBuffer's iterator should iterate its kv pairs in the same order as snapshot
	Snapshot
	// Set associates key with value
	Set([]byte, []byte) error
}

// UnionStore is an implement of Store which contains a buffer for update.
type UnionStore struct {
	Dirty    MemBuffer // updates are buffered in memory
	Snapshot Snapshot  // for read
}

// NewUnionStore builds a new UnionStore.
func NewUnionStore(snapshot Snapshot) (UnionStore, error) {
	dirty := p.Get().(MemBuffer)
	return UnionStore{
		Dirty:    dirty,
		Snapshot: snapshot,
	}, nil
}

// Get implements the Store Get interface.
func (us *UnionStore) Get(key []byte) (value []byte, err error) {
	// Get from update records frist
	value, err = us.Dirty.Get(key)
	if IsErrNotFound(err) {
		// Try get from snapshot
		return us.Snapshot.Get(key)
	}
	if err != nil {
		return nil, errors.Trace(err)
	}

	if len(value) == 0 { // Deleted marker
		return nil, errors.Trace(ErrNotExist)
	}

	return value, nil
}

// Set implements the Store Set interface.
func (us *UnionStore) Set(key []byte, value []byte) error {
	return us.Dirty.Set(key, value)
}

// Seek implements the Snapshot Seek interface.
func (us *UnionStore) Seek(key []byte, txn Transaction) (Iterator, error) {
	snapshotIt := us.Snapshot.NewIterator(key)
	dirtyIt := us.Dirty.NewIterator(key)
	it := newUnionIter(dirtyIt, snapshotIt)
	return it, nil
}

// Delete implements the Store Delete interface.
func (us *UnionStore) Delete(k []byte) error {
	// Mark as deleted
	val, err := us.Dirty.Get(k)
	if err != nil {
		if !IsErrNotFound(err) { // something wrong
			return errors.Trace(err)
		}

		// missed in dirty
		val, err = us.Snapshot.Get(k)
		if err != nil {
			if IsErrNotFound(err) {
				return errors.Trace(ErrNotExist)
			}
		}
	}

	if len(val) == 0 { // deleted marker, already deleted
		return errors.Trace(ErrNotExist)
	}

	return us.Dirty.Set(k, nil)
}

// Close implements the Store Close interface.
func (us *UnionStore) Close() error {
	us.Snapshot.Release()
	us.Dirty.Release()
	p.Put(us.Dirty)
	return nil
}
