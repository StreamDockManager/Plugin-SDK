"""Read-only discovery coordinator. Use the SDK bounded background executor;
complete/expire on the owner loop and clear on context removal. No command retries.
"""
from dataclasses import dataclass
import json
import threading
from .managedstate import _encoded
MAX_PENDING=32
MAX_RESPONSE_BYTES=256<<10
@dataclass(frozen=True)
class Request:
    context:str
    channel:str
    id:str
    operation:str
    action:str=''
    scope:str='action'
@dataclass(frozen=True)
class Ticket:
    request:Request
    deadline:int
    cancel:threading.Event
@dataclass(frozen=True)
class Reply:
    context:str
    action:str
    payload:dict
# Return field for discovery
def _field(s,maximum,empty=False):return isinstance(s,str) and (empty or bool(s)) and len(s.encode())<=maximum
# Validate discovery inputs before further processing
def _validate(r):
    if not _field(r.context,1024) or not _field(r.action,1024,True) or not _field(r.channel,128) or not _field(r.id,128) or not _field(r.operation,128) or r.scope not in ('action','plugin'):raise ValueError('invalid discovery request')
# Return time for discovery
def _time(v):return type(v) is int and 0<=v<=9007199254740991
# Build the discovery response envelope
def envelope(request,payload=None,failure=''):
    _validate(request);result=dict(payload or {});result.update(op=request.operation,request_id=request.id,scope=request.scope,available=not failure)
    if failure:result['error']=failure;result.setdefault('message',dict(timeout='Discovery timed out',busy='Discovery is busy; try again shortly',failed='Discovery failed').get(failure,'Discovery failed'))
    try:
        encoded=_encoded(result)
        if len(encoded)>MAX_RESPONSE_BYTES:raise ValueError('oversized response')
    except (ValueError,TypeError,OverflowError):return envelope(request,{'message':'Discovery response exceeds its supported size'},'response_too_large')
    return Reply(request.context,request.action,json.loads(encoded))
class Runner:
    # Initialize pending discovery requests and lifecycle state
    def __init__(self):self._pending={};self._closed=False
    # Start runner data
    def start(self,request,now,timeout):
        _validate(request)
        if self._closed or not _time(now) or type(timeout) is not int or not 0<timeout<=120000 or not _time(now+timeout):raise ValueError('invalid discovery deadline or closed runner')
        key=(request.context,request.channel)
        if key not in self._pending and len(self._pending)>=MAX_PENDING:raise RuntimeError('discovery queue full')
        if key in self._pending:self._pending[key].cancel.set()
        ticket=Ticket(request,now+timeout,threading.Event());self._pending[key]=ticket;return ticket
    # Cancel runner data
    def cancel(self,context,channel,id):
        key=(context,channel);ticket=self._pending.get(key)
        if ticket is None or ticket.request.id!=id:return False
        ticket.cancel.set();del self._pending[key];return True
    # Clear runner data
    def clear(self,context):
        for t in list(self._pending.values()):
            if t.request.context==context:self.cancel(context,t.request.channel,t.request.id)
    # Stop runner activity and release owned resources
    def close(self):
        for t in self._pending.values():t.cancel.set()
        self._pending.clear();self._closed=True
    # Return the earliest pending work deadline
    def next_deadline(self):return min((t.deadline for t in self._pending.values()),default=0)
    # Record completion of the pending runner operation
    def complete(self,ticket,payload,failure,now):
        if not _time(now):raise ValueError('invalid discovery time')
        key=(ticket.request.context,ticket.request.channel)
        if self._pending.get(key) is not ticket:return None
        if now>=ticket.deadline:payload=None;failure='timeout'
        elif ticket.cancel.is_set():del self._pending[key];return None
        reply=envelope(ticket.request,payload,failure);ticket.cancel.set();del self._pending[key];return reply
    # Expire stale scheduled state before the next operation
    def expire(self,now):
        if not _time(now):raise ValueError('invalid discovery time')
        tickets=sorted((t for t in self._pending.values() if t.deadline<=now),key=lambda t:(t.deadline,t.request.context,t.request.channel))
        return [reply for t in tickets if (reply:=self.complete(t,None,'timeout',now)) is not None]
