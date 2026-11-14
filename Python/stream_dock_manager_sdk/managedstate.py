"""Managed JSON records with explicit session, portable or private storage.

Instantiate PortableSettings or HostStore yourself. Unsupported private storage is
never silently replaced with portable settings or a local file.
"""
import hashlib
import json
import threading
from .hoststorage import RPCError, operation_id

# Serialize the value using the module’s stable JSON representation
def _encoded(value):return json.dumps(value,ensure_ascii=False,sort_keys=True,separators=(',',':'),allow_nan=False).encode()
# Copy the current state for an isolated reader
def _snapshot(value):
    text=_encoded(value)
    if len(text)>8<<20:raise RPCError('quota','managed state exceeds 8 MiB')
    result=json.loads(text)
    if not isinstance(result,dict) or any(not key for key in result):raise ValueError('managed state must be an object with nonempty record identities')
    if len(result)>4096:raise RPCError('quota','managed state exceeds 4096 records')
    return result

# Return key for managedstate
def _key(identity):return 'record:'+hashlib.sha256(identity.encode()).hexdigest()
# Return version for managedstate
def _version(value):return dict(generation=value['generation'],revision=value['revision'])

class ManagedState:
    # Initialize the chosen backend and guarded persistence state
    def __init__(self,mode,backend=None,key='_runtime_v1'):
        if mode not in ('session','portable','private') or mode!='session' and backend is None or mode=='portable' and not key:
            raise ValueError('managed state requires a valid mode and explicit backend')
        self.mode,self.backend,self.key=mode,backend,key
        self.dirty=False;self.assurance='';self._ready=False;self._written={};self._pending=None;self._blocked=None;self._version=None
        self._lock=threading.Lock()
    # Load managed state data
    def load(self,cancel=None):
        with self._lock:
            if self._ready:return _snapshot(self._written)
            value={}
            if self.mode=='portable':
                result=self.backend.read(cancel);self.assurance=result['assurance']
                if self.key in result['settings']:value=_snapshot(result['settings'][self.key])
            elif self.mode=='private':
                cursor='';seen=set();total=0
                while True:
                    if cursor in seen or len(seen)>=4096:raise ValueError('repeated storage cursor or restore page budget exceeded')
                    seen.add(cursor);page=self.backend.list_keys(cursor,cancel)
                    if not cursor:self._version=_version(page)
                    elif _version(page)!=self._version:raise RPCError('conflict','private state changed during load')
                    for name in page['keys']:
                        item=self.backend.get(name,cancel)
                        if not item['found'] or _version(item)!=self._version:raise RPCError('conflict','private state changed during load')
                        record=item['value'];total+=len(_encoded(record))
                        if total>8<<20 or len(value)>=4096:raise RPCError('quota','managed state exceeds restore budget')
                        if not isinstance(record,dict) or not isinstance(record.get('identity'),str) or not record['identity'] or 'value' not in record or _key(record['identity'])!=name or record['identity'] in value:raise ValueError('invalid managed state record')
                        value[record['identity']]=record['value']
                    cursor=page.get('cursor','')
                    if not cursor:break
                self.assurance='observed'
            else:self.assurance='session'
            self._written=_snapshot(value);self._ready=True;return _snapshot(value)
    # Mark managed state for a deferred persistence attempt
    def mark_dirty(self):
        with self._lock:self.dirty=True
    # Check pending
    def check_pending(self):
        if self.dirty and self.mode!='session':raise RuntimeError('managed state has an unconfirmed write')
    # Save managed state data
    def save(self,value,cancel=None):
        with self._lock:
            if not self._ready:raise RuntimeError('managed state is not initialized')
            if self._blocked:raise self._blocked
            current=_snapshot(value);self.dirty=True
            if self.mode=='session':self._written=current;self.dirty=False;return
            if self.mode=='portable':
                if _encoded(current)!=_encoded(self._written):
                    if len(_encoded(current))>256<<10:raise RPCError('quota','portable runtime state exceeds 256 KiB')
                    result=self.backend.update({self.key:current},cancel);self.assurance=result['assurance']
                self._written=current;self.dirty=False;return
            if self._pending:
                pending=self._pending;receipt=self.backend.receipt(pending['version']['generation'],pending['id'],cancel)
                if receipt.get('found') and receipt.get('committed'):
                    self._version=_version(receipt);self.assurance='committed';self._written=pending['snapshot'];self._pending=None
            if self._pending is None:
                keys=sorted(key for key in current.keys()|self._written.keys() if (key in current)!=(key in self._written) or _encoded(current.get(key))!=_encoded(self._written.get(key)))
                if len(keys)>32:raise RPCError('quota','managed state change exceeds one atomic 32-record commit')
                if not keys:self.dirty=False;return
                mutations=[dict(op='put',key=_key(key),value=dict(identity=key,value=current[key])) if key in current else dict(op='delete',key=_key(key)) for key in keys]
                for mutation in mutations:
                    if mutation['op']=='put' and len(_encoded(mutation['value']))>256<<10:raise RPCError('quota','managed state record exceeds 256 KiB')
                self._pending=dict(id=operation_id(),version=dict(self._version),mutations=mutations,snapshot=current)
            pending=self._pending
            try:receipt=self.backend.commit(pending['version'],pending['id'],pending['mutations'],cancel)
            except RPCError as error:
                if error.code=='conflict':self._blocked=error
                raise
            self._version=_version(receipt);self.assurance='committed';self._written=pending['snapshot'];self._pending=None
            self.dirty=_encoded(current)!=_encoded(self._written)
