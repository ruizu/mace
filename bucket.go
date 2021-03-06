package mace

import (
	"container/heap"
	"log"
	"sync"
	"time"
)

type MaceBucket struct {
	sync.RWMutex
	name         string
	items        map[string]*MaceItem
	leakqueue    *leakQueue
	leakTimer    *time.Timer
	leakInterval time.Duration
	logger       *log.Logger
	loadItems    func(string) *MaceItem
	onAddItem    func(*MaceItem)
	onDeleteItem func(*MaceItem)
}

func (bucket *MaceBucket) Count() int {
	bucket.RLock()
	defer bucket.RUnlock()
	return len(bucket.items)
}

func (bucket *MaceBucket) SetDataLoader(f func(string) *MaceItem) {
	bucket.Lock()
	defer bucket.Unlock()
	bucket.loadItems = f
}

func (bucket *MaceBucket) SetOnAddItem(f func(*MaceItem)) {
	bucket.Lock()
	defer bucket.Unlock()
	bucket.onAddItem = f
}

func (bucket *MaceBucket) SetOnDeleteItem(f func(*MaceItem)) {
	bucket.Lock()
	defer bucket.Unlock()
	bucket.onDeleteItem = f
}

func (bucket *MaceBucket) SetLogger(logger *log.Logger) {
	bucket.Lock()
	defer bucket.Unlock()
	bucket.logger = logger
}

func (bucket *MaceBucket) leakCheck() {
	bucket.Lock()
	if bucket.leakTimer != nil {
		bucket.leakTimer.Stop()
	}
	if bucket.leakInterval > 0 {
		bucket.log("Expiration check triggered after " + bucket.leakInterval.String() + " for bucket" + bucket.name)
	} else {
		bucket.log("Expiration check installed on bucket", bucket.name)
	}
	invalidL := []*disposeItem{}
	cur := time.Now()
	l := bucket.leakqueue
	for {
		if l.Len() > 0 {
			if it := heap.Pop(l); cur.Sub(it.(*disposeItem).disposeTime) >= 0 {
				invalidL = append(invalidL, it.(*disposeItem))
			} else {
				heap.Push(l, (it.(*disposeItem)))
				break
			}
			break

		}
	}
	bucket.Unlock()

	// fetch current time for comparison
	// used to create next timer callback

	// Change this to Heap so that cleaning is after
	// at expense of more space usage
	// Per item timestamp + pointer to item
	for _, itemP := range invalidL {
		key := itemP.value
		bucket.Delete(key)
	}
	bucket.Lock()
	if bucket.leakqueue.Len() > 0 {
		itemMin := heap.Pop(bucket.leakqueue).(*disposeItem)
		dur := itemMin.disposeTime
		bucket.leakInterval = dur.Sub(cur)
		bucket.leakTimer = time.AfterFunc(bucket.leakInterval, func() {
			go bucket.leakCheck()
		})
		heap.Push(bucket.leakqueue, itemMin)
	}
	bucket.Unlock()

}

func (bucket *MaceBucket) Delete(key string) (*MaceItem, error) {
	bucket.Lock()

	v, ok := bucket.items[key]
	if !ok {
		bucket.Unlock()
		return nil, ErrKeyNotFound
	}
	deleteCallback := bucket.onDeleteItem
	bucket.Unlock()
	if deleteCallback != nil {
		// TODO: clone item before calling this routine
		// Secondary advantage is ablility to run this as separate
		// go routine
		deleteCallback(v)
	}
	bucket.Lock()
	defer bucket.Unlock()
	bucket.log("Deleting item with key: " + key + " created on " + v.Created().String())
	delete(bucket.items, key)
	return v, nil
}

func (bucket *MaceBucket) Cache(key string, alive time.Duration,
	data interface{}) *MaceItem {
	item := NewMaceItem(key, data, alive)
	bucket.Lock()
	bucket.log("Adding item with key: " + key +
		" which will be alive for:" + alive.String())
	bucket.items[key] = item
	if item.alive != 0 {
		heap.Push(bucket.leakqueue, item.dispose)
	}
	expiry := bucket.leakInterval
	addCallback := bucket.onAddItem
	bucket.Unlock()

	if addCallback != nil {
		// TODO: clone item and call addCallback as a go routine
		addCallback(item)
	}
	// Leak check set or run
	if alive > 0 && (expiry == 0 || alive < expiry) {
		bucket.leakCheck()
	}

	return item
}

func (bucket *MaceBucket) Exists(key string) bool {
	bucket.RLock()
	defer bucket.RUnlock()
	_, ok := bucket.items[key]
	return ok
}

func (bucket *MaceBucket) Value(key string) (*MaceItem, error) {
	bucket.RLock()
	v, ok := bucket.items[key]
	loadItems := bucket.loadItems
	bucket.RUnlock()
	if ok {
		v.KeepAlive()
		// We care to update LeakQueue only if it has Alive duration
		// set
		if v.Alive() != 0 {
			bucket.Lock()
			bucket.leakqueue.update(v.dispose)
			bucket.Unlock()
		}
		return v, nil
	}
	if loadItems != nil {
		item := loadItems(key)
		if item != nil {
			bucket.Cache(key, item.Alive(), item.data)
			return item, nil
		}
		return nil, ErrKeyNotFoundOrLoadable
	}
	return nil, ErrKeyNotFound
}

func (bucket *MaceBucket) Flush() {
	bucket.Lock()
	defer bucket.Unlock()
	bucket.log("Flushing the cache bucket: " + bucket.name)
	bucket.items = make(map[string]*MaceItem)
	l := leakQueue{}
	heap.Init(&l)
	bucket.leakqueue = &l
	bucket.leakInterval = 0
	if bucket.leakTimer != nil {
		bucket.leakTimer.Stop()
	}
	return
}

func (bucket *MaceBucket) log(v ...interface{}) {
	if bucket.logger == nil {
		return
	}
	bucket.logger.Println(v)
}
