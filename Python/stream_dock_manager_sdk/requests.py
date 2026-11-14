"""Bounded shared JSON reads; cancellation belongs to each consumer."""
import json
import math
import threading
import time
class RequestGroup:
 # Initialize shared in-flight requests, cooldowns, and admission slots
 def __init__(self):
  self._lock=threading.Lock();self._flights={};self._cooldowns={};self._slots=0;self._closed=False
 # work(cancel_event) returns JSON. retry_after(error) returns milliseconds
 def get(self,key,work,cancel=None,retry_after=lambda error:0):
  """work(cancel_event) returns JSON. retry_after(error) returns milliseconds."""
  if cancel is not None and cancel.is_set():raise RuntimeError('shared read cancelled')
  if not isinstance(key,str) or not key or len(key.encode())>2048:raise ValueError('invalid shared read key')
  with self._lock:
   if self._closed:raise RuntimeError('shared read group closed')
   now=time.monotonic();self._cooldowns={k:v for k,v in self._cooldowns.items() if v[0]>now}
   if key in self._cooldowns:raise self._cooldowns[key][1]
   flight=self._flights.get(key)
   if flight is None:
    if self._slots+len(self._cooldowns)>=32:raise RuntimeError('shared read capacity exhausted')
    flight=dict(cancel=threading.Event(),done=threading.Event(),users=0,finished=False,retired=False,data=None,error=None,deadline=now+8)
    self._flights[key]=flight;self._slots+=1
    # Drive the request group execution loop
    def run():
     data=error=None;delay=0
     try:
      value=work(flight['cancel']);data=json.dumps(value,ensure_ascii=False,allow_nan=False,separators=(',',':')).encode()
      if len(data)>1<<20:raise ValueError('shared read exceeds 1 MiB')
     except Exception as failure:
      error=failure
      try:delay=retry_after(error)
      except Exception:delay=0
     with self._lock:
      flight.update(data=data,error=error,finished=True)
      if flight['users']==0:self._slots-=1
      if self._flights.get(key) is flight and not self._closed and not flight['cancel'].is_set() and time.monotonic()<flight['deadline'] and isinstance(delay,(float,int)) and math.isfinite(delay) and delay>0:
       self._cooldowns[key]=(time.monotonic()+min(delay,9223372036854)/1000,error)
      flight['done'].set()
    threading.Thread(target=run,daemon=True,name='sdk-shared-read').start()
   flight['users']+=1
  try:
   while not flight['done'].wait(.01):
    if time.monotonic()>=flight['deadline']:flight['cancel'].set()
    if flight['cancel'].is_set() or cancel is not None and cancel.is_set():raise RuntimeError('shared read cancelled or timed out')
   with self._lock:
    if time.monotonic()>=flight['deadline']:flight['cancel'].set()
    if self._closed or flight['retired'] or flight['cancel'].is_set() or cancel is not None and cancel.is_set():raise RuntimeError('shared read cancelled')
    if flight['error'] is not None:raise flight['error']
    return json.loads(flight['data'])
  finally:
   with self._lock:
    flight['users']-=1
    if flight['users']==0:
     if self._flights.get(key) is flight:self._flights.pop(key)
     flight['cancel'].set()
     if flight['finished']:self._slots-=1
 # Cancel pending
 def cancel_pending(self):
  with self._lock:self._cancel_pending()
 # Cancel pending
 def _cancel_pending(self):
  for flight in self._flights.values():flight['retired']=True;flight['cancel'].set()
  self._flights.clear()
 # Stop request group activity and release owned resources
 def close(self):
  with self._lock:self._closed=True;self._cancel_pending();self._cooldowns.clear()
