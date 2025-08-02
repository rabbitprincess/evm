package mpt

// var (
// 	errNotSupported = errors.New("not supported")
// )

// var _ ethdb.Database = (*DBWrapper)(nil)
// var _ ethdb.KeyValueStore = (*DBWrapper)(nil)

// type DBWrapper struct {
// 	dbm.DB
// }

// func (w *DBWrapper) Put(key []byte, value []byte) error {
// 	return w.Set(key, value)
// }

// func (w *DBWrapper) NewBatch() ethdb.Batch {
// 	return &BatchWrapper{batch: w.DB.NewBatch()}
// }

// func (w *DBWrapper) NewBatchWithSize(size int) ethdb.Batch {
// 	return &BatchWrapper{batch: w.DB.NewBatch()}
// }

// func (w *DBWrapper) DeleteRange(start, end []byte) error {
// 	it, err := w.Iterator(start, end)
// 	if err != nil {
// 		return err
// 	}
// 	defer it.Close()

// 	for ; it.Valid(); it.Next() {
// 		if err := w.Delete(it.Key()); err != nil {
// 			return err
// 		}
// 	}
// 	return nil
// }

// var _ ethdb.Batch = (*BatchWrapper)(nil)

// type BatchWrapper struct {
// 	batch dbm.Batch
// }

// func (b *BatchWrapper) Put(key, value []byte) error {
// 	return b.batch.Set(key, value)
// }

// func (b *BatchWrapper) Delete(key []byte) error {
// 	return b.batch.Delete(key)
// }

// func (b *BatchWrapper) ValueSize() int {
// 	return 0
// }

// func (b *BatchWrapper) Write() error {
// 	return b.batch.Write()
// }

// func (b *BatchWrapper) Reset() {
// 	_ = b.batch.Close()
// }

// func (b *BatchWrapper) Replay(w ethdb.KeyValueWriter) error {
// 	return errNotSupported
// }

// func (b *BatchWrapper) Close() {
// 	_ = b.batch.Close()
// }
