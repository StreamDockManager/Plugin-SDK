"""Bounded service WebSockets. Authentication and protocol envelopes stay in plugins."""
import json
import socket
import threading
import time
from urllib.parse import urlsplit
from wsproto.events import Request, AcceptConnection
from .wire import BoundedSocket, MAX_UPGRADE
from .network import NetworkPayload
MAX_FRAME=4<<20
MAX_REQUEST=64<<10
class Rejected(RuntimeError):
 # Preserve the connection rejection reason in the error message
 def __init__(self,operation):super().__init__(operation+' was rejected; check permissions and the selected resource')
# Convert a connector failure into the public command error shape
def command_error(error):
 if isinstance(error,Rejected):return error
 result=RuntimeError('command delivery is uncertain; check the server state before trying again');result.__cause__=error;return result
# Return closed for connectedws
def _closed():return ConnectionError('WebSocket connection closed; reconnect to refresh state')
# Encode connectedws data
def _encode(value):
 data=json.dumps(value,ensure_ascii=False,allow_nan=False,separators=(',',':'))
 if len(data.encode())>MAX_REQUEST:raise ValueError('invalid or oversized service request')
 return data
class Channel:
 # Bind the wire connection to its operation deadline
 def __init__(self,wire,deadline):self.wire=wire;self.deadline=deadline
 # Send the supplied channel message
 def send(self,value):self.wire.send(_encode(value))
 # Receive the next channel message
 def receive(self):
  while True:
   try:
    raw=self.wire.recv()
    if not isinstance(raw,str) or not raw:raise _closed()
    return json.loads(raw)
   except socket.timeout:
    if self.deadline is None or time.monotonic()>=self.deadline:raise
 # Stop channel activity and release owned resources
 def close(self):self.wire.abort()
class Session:
 # Start response routing and track pending calls on the channel
 def __init__(self,channel,route,closed=None):
  self.channel,self.route,self.on_closed=channel,route,closed
  self._lock=threading.Lock();self._write=threading.Lock();self._pending={};self._closed=threading.Event()
  channel.deadline=None
  self._thread=threading.Thread(target=self._read,daemon=True,name='sdk-service-websocket');self._thread.start()
 # Report whether the underlying worker or connection remains active
 @property
 def alive(self):return not self._closed.is_set()
 # Stop session activity and release owned resources
 def close(self):
  with self._lock:
   if self._closed.is_set():return
   self._closed.set();pending=list(self._pending.values());self._pending.clear()
  self.channel.close()
  for item in pending:item['error']=_closed();item['ready'].set()
  if self.on_closed:
   try:self.on_closed()
   except Exception:pass
 # Submit the session request and wait for its response
 def request(self,id,value,cancel=None):
  if cancel is not None and cancel.is_set():raise RuntimeError('service request cancelled')
  if not isinstance(id,str) or not id or len(id.encode())>128:raise ValueError('invalid service request ID')
  text=_encode(value);pending=dict(ready=threading.Event(),value=None,error=None)
  with self._lock:
   if not self.alive:raise _closed()
   if id in self._pending or len(self._pending)>=32:raise RuntimeError('duplicate request ID or service request queue full')
   self._pending[id]=pending
  deadline=time.monotonic()+8
  try:
   with self._write:
    if not self.alive:raise _closed()
    if cancel is not None and cancel.is_set():raise RuntimeError('service request cancelled')
    try:self.channel.wire.send(text)
    except Exception:self.close();raise _closed()
   while not pending['ready'].wait(.01):
    if time.monotonic()>=deadline:raise TimeoutError('service request timed out')
    if cancel is not None and cancel.is_set():raise RuntimeError('service request cancelled')
   if pending['error']:raise pending['error']
   return pending['value']
  finally:
   with self._lock:self._pending.pop(id,None)
 # Read the next session value or data segment
 def _read(self):
  ping=time.monotonic()+30
  try:
   while self.alive:
    now=time.monotonic()
    if now-self.channel.wire.last_pong>=90:break
    if now>=ping:self.channel.wire.ping();ping=now+30
    try:value=self.channel.receive()
    except socket.timeout:continue
    id=self.route(value)
    if id:
     with self._lock:
      pending=self._pending.get(id)
      if pending and not pending['ready'].is_set():pending['value']=value;pending['ready'].set()
  except Exception:pass
  finally:self.close()
class ServiceClient:
 # Initialize connection generations and cancellation state
 def __init__(self,network=None):
  self.network=network or NetworkPayload();self._lock=threading.Lock();self._opening=threading.Lock();self._generation=0;self._session=None;self._key='';self._cancel=None
 # Stop service client activity and release owned resources
 def close(self):
  with self._lock:
   self._generation+=1;session,cancel=self._session,self._cancel;self._session=self._cancel=None;self._key=''
  if cancel:cancel()
  if session:session.close()
 # Establish the validated transport connection
 def connect(self,key,endpoint,handshake,route,cancel=None,closed=None):
  if cancel is not None and cancel.is_set():raise RuntimeError('service connection cancelled')
  if not self._opening.acquire(blocking=False):raise RuntimeError('WebSocket connection already opening')
  transport=None;watch=None;stop=threading.Event()
  try:
   with self._lock:
    if self._key==key and self._session and self._session.alive:return self._session,False
   url=urlsplit(endpoint)
   if url.scheme not in ('ws','wss') or not url.hostname or url.username is not None or url.password is not None or url.query or url.fragment:raise ValueError('use ws:// or wss:// without credentials or query parameters')
   self.close();deadline=time.monotonic()+10
   with self._lock:generation=self._generation
   holder=[];interrupted=threading.Event()
   # Interrupt the active socket operation and release its connection
   def abort():
    interrupted.set()
    for connection in holder:
     try:connection.shutdown(socket.SHUT_RDWR)
     except OSError:pass
     connection.close()
   with self._lock:self._cancel=abort
   # Watch cancellation and connection lifetime for the service client
   def monitor():
    while not stop.wait(.01):
     if time.monotonic()>=deadline or cancel is not None and cancel.is_set():abort();return
   watch=threading.Thread(target=monitor,daemon=True,name='sdk-service-connect');watch.start()
   try:
    transport=socket.create_connection((url.hostname,url.port or (443 if url.scheme=='wss' else 80)),timeout=8);holder.append(transport)
    if interrupted.is_set():raise _closed()
    if url.scheme=='wss':
     import ssl
     context=ssl.create_default_context();context.minimum_version=ssl.TLSVersion.TLSv1_2
     transport=context.wrap_socket(transport,server_hostname=url.hostname,do_handshake_on_connect=False);holder.append(transport);transport.do_handshake()
    transport.settimeout(.1);tracked=self.network.track(transport);wire=BoundedSocket(tracked,MAX_FRAME)
    tracked.sendall(wire._protocol.send(Request(host=url.netloc,target=url.path or '/')))
    response=bytearray()
    while b'\r\n\r\n' not in response:
     if time.monotonic()>=deadline:raise TimeoutError('service upgrade timed out')
     try:data=tracked.recv(4096)
     except socket.timeout:continue
     if not data:raise _closed()
     response.extend(data)
     if len(response)>MAX_UPGRADE:raise ValueError('service upgrade exceeds limit')
    end=response.index(b'\r\n\r\n')+4;wire._protocol.receive_data(bytes(response[:end]))
    if not any(isinstance(event,AcceptConnection) for event in wire._protocol.events()):raise _closed()
    if end<len(response):wire._receive(bytes(response[end:]))
   except Exception as error:raise ConnectionError('WebSocket connection failed; check the server URL and network') from error
   channel=Channel(wire,deadline)
   if interrupted.is_set():raise _closed()
   handshake(channel)
   with self._lock:
    if generation!=self._generation or time.monotonic()>=deadline or cancel is not None and cancel.is_set():raise _closed()
    session=Session(channel,route,closed);self._session=session;self._key=key;self._cancel=None
   transport=None;return session,True
  finally:
   stop.set()
   if transport is not None:transport.close()
   if watch:watch.join(1)
   with self._lock:self._cancel=None
   self._opening.release()
