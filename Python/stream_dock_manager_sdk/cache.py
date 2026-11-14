"""Owner-serialized LRU caches. Values are immutable caller-owned snapshots.
Costs measure logical retained bytes, not process RSS. Persistence is explicit.
"""
from collections import OrderedDict
from dataclasses import dataclass
import json
import threading
from .managedstate import ManagedState, _encoded
MAX_ENTRIES=65536
MAX_BYTES=64<<20
MAX_TIME=9007199254740991
# Return time for cache
def _time(value):return type(value) is int and 0<=value<=MAX_TIME
@dataclass(frozen=True)
class Entry:
    key:str
    value:object
    cost:int
    expires:int
    # Expose the charge value from entry
    @property
    def charge(self):return self.cost+len(self.key.encode())+64
class Cache:
    # Initialize the entry and byte budgets for cached values
    def __init__(self,capacity,limit):
        if type(capacity) is not int or not 0<capacity<=MAX_ENTRIES or type(limit) is not int or not 0<limit<=MAX_BYTES:raise ValueError('invalid cache limits')
        self.capacity,self.limit=capacity,limit;self._values=OrderedDict();self.bytes=0
    # Return the number of retained cache entries
    def __len__(self):return len(self._values)
    # Delete cache data
    def delete(self,key):
        entry=self._values.pop(key,None)
        if entry is None:return False
        self.bytes-=entry.charge;return True
    # Clear cache data
    def clear(self):self._values.clear();self.bytes=0
    # Read a cached entry without changing its eviction order
    def peek(self,key,now):
        if not _time(now):return None
        entry=self._values.get(key)
        if entry is not None and entry.expires and entry.expires<=now:self.delete(key);return None
        return entry.value if entry is not None else None
    # Read a cached value and update its recency for eviction
    def get(self,key,now):
        value=self.peek(key,now)
        if _time(now) and key in self._values:self._values.move_to_end(key)
        return value
    # Return a snapshot of the retained cache entries
    def entries(self,now):
        if not _time(now):raise ValueError('invalid cache time')
        for e in list(self._values.values()):
            if e.expires and e.expires<=now:self.delete(e.key)
        return list(self._values.values())
    # Evict cache entries until the configured budgets are met
    def trim(self,reserved,now):
        if type(reserved) is not int or not 0<=reserved<=self.limit or not _time(now):raise ValueError('invalid cache reservation')
        evicted=sorted(e.key for e in self._values.values() if e.expires and e.expires<=now)
        for key in evicted:self.delete(key)
        for e in list(self._values.values()):
            if self.bytes+reserved<=self.limit and len(self)<=self.capacity:break
            self.delete(e.key);evicted.append(e.key)
        return evicted
    # Store the supplied cache entry
    def put(self,key,value,cost,expires,now):
        if not isinstance(key,str) or not key or len(key.encode())>4096 or type(cost) is not int or cost<0 or cost+len(key.encode())+64>self.limit or not _time(expires) or not _time(now):raise ValueError('invalid or oversized cache entry')
        self.delete(key);evicted=self.trim(0,now)
        if expires and expires<=now:return evicted
        entry=Entry(key,value,cost,expires);self._values[key]=entry;self.bytes+=entry.charge
        return evicted+self.trim(0,now)
class Budget:
    # Initialize synchronized accounting for reserved capacity
    def __init__(self,limit):
        if type(limit) is not int or not 0<limit<=MAX_BYTES:raise ValueError('invalid byte budget')
        self.limit=limit;self._used=0;self._lock=threading.Lock()
    # Expose the used value from budget
    @property
    def used(self):
        with self._lock:return self._used
    # Reserve bounded capacity before admitting new work
    def reserve(self,size):
        with self._lock:
            if type(size) is not int or not 0<=size<=self.limit-self._used:return False
            self._used+=size;return True
    # Release budget data
    def release(self,size):
        with self._lock:
            if type(size) is not int or not 0<=size<=self._used:raise ValueError('invalid budget release')
            self._used-=size
class Persistence:
    """Private host storage; atomic restore, batched flush, session-local LRU order."""
    # Initialize pending cache persistence and its synchronization lock
    def __init__(self,backend):self._state=ManagedState('private',backend);self._ready=False;self._pending=None;self._lock=threading.Lock()
    # Load persistence data
    def load(self,cache,now,cancel=None):
        with self._lock:
            if self._ready or cache.capacity>4096 or cache.limit>8<<20 or not _time(now):raise ValueError('invalid persistent cache state or limits')
            records=self._state.load(cancel);temporary=Cache(cache.capacity,cache.limit)
            for key in sorted(records):
                e=records[key]
                if not isinstance(e,dict) or 'value' not in e or type(e.get('cost')) is not int or e['cost']<len(_encoded(e['value'])):raise ValueError('invalid persistent cache entry')
                temporary.put(key,e['value'],e['cost'],e.get('expires'),now)
            cache.clear()
            for e in temporary.entries(now):cache.put(e.key,e.value,e.cost,e.expires,now)
            self._ready=True
    # Flush persistence data
    def flush(self,cache,now,cancel=None):
        with self._lock:
            if not self._ready or not _time(now):raise ValueError('cache persistence is not loaded or time is invalid')
            target={}
            for e in cache.entries(now):
                if e.cost<len(_encoded(e.value)):raise ValueError('invalid persistent cache cost')
                target[e.key]=dict(value=e.value,cost=e.cost,expires=e.expires)
            target=json.loads(_encoded(target))
            if any(len(_encoded(dict(identity=k,value=v)))>256<<10 for k,v in target.items()):raise ValueError('persistent cache record exceeds 256 KiB')
            if self._pending is not None:self._state.save(self._pending,cancel);self._pending=None
            for _ in range(8193):
                if cancel is not None and cancel.is_set():raise RuntimeError('cache persistence cancelled')
                written=self._state.load(cancel)
                keys=sorted(k for k in written if k not in target)+sorted(k for k in target if k not in written or _encoded(target[k])!=_encoded(written[k]))
                if not keys:return
                batch_bytes=0
                for key in keys[:32]:
                    record_bytes=len(_encoded(dict(identity=key,value=target[key]))) if key in target else 0
                    if record_bytes>256<<10:raise ValueError('persistent cache record exceeds 256 KiB')
                    size=record_bytes+256
                    if batch_bytes+size>400<<10:break
                    batch_bytes+=size
                    if key in target:written[key]=target[key]
                    else:del written[key]
                self._pending=written;self._state.save(written,cancel);self._pending=None
            raise RuntimeError('cache flush exceeded batch budget')
