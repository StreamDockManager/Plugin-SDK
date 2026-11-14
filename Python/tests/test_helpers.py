import unittest,json,sys,base64
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];sys.path.insert(0,str(ROOT/'Python'))
from stream_dock_manager_sdk.cache import Cache,Budget
from stream_dock_manager_sdk.operations import Ledger
from stream_dock_manager_sdk.discovery import Runner,Request,envelope,MAX_PENDING,MAX_RESPONSE_BYTES
from stream_dock_manager_sdk import thumbnail as image
SPEC=json.loads((ROOT/'contracts/fixtures/helpers-v1.json').read_text())
class HelperTests(unittest.TestCase):
 # Verify shared cache
 def test_shared_cache(self):
  s=SPEC['cache'];c=Cache(s['capacity'],s['bytes']);a,b,d=s['keys'];c.put(a,'A',s['cost'],0,0);c.put(b,'B',s['cost'],0,0);c.get(a,1);self.assertEqual(c.put(d,'C',s['cost'],s['expiry'],2),[b])
  with self.assertRaises(ValueError):c.put(a,'bad',1000,0,3)
  self.assertEqual(c.peek(a,3),'A');self.assertIsNone(c.get(d,s['expiry']));self.assertEqual(c.bytes,75);self.assertEqual(c.trim(s['bytes'],31),[a]);self.assertEqual(c.bytes,0)
  b=Budget(100);self.assertTrue(b.reserve(80));self.assertFalse(b.reserve(21))
  with self.assertRaises(ValueError):b.release(81)
  self.assertEqual(b.used,80);b.release(80)
 # Verify shared operations
 def test_shared_operations(self):
  s=SPEC['operations'];l=Ledger(s['capacity'],s['bytes']);a=l.reserve(s['group'],'first',s['cost']);b=l.reserve(s['group'],'second',s['cost']);c=l.reserve('other','third',s['cost']);self.assertEqual([a.id,b.id,c.id],s['ids']);l.update(b.id,'uncertain','unknown',s['cost'])
  with self.assertRaises(ValueError):l.reserve('fourth','no',s['cost'])
  self.assertEqual(l.retire(set()),[]);l.update(a.id,'confirmed','done',s['cost'],True);self.assertEqual(l.retire(set()),[]);l.update(a.id,'confirmed','done',s['cost']);self.assertEqual(l.retire({s['group']}),[]);self.assertEqual(l.retire(set()),[a.id]);before=l.bytes
  with self.assertRaises(ValueError):l.update(b.id,'confirmed','too big',s['bytes'])
  self.assertEqual(l.bytes,before);self.assertEqual(l.get(b.id).state,'uncertain');l.reserve('fourth','ok',s['cost'])
 # Verify discovery requires current ids
 def test_discovery_requires_current_ids(self):
  s=SPEC['discovery']
  for id in s['invalid_ids']+[None,1]:
   req=Request(s['context'],s['channel'],id,s['operation'])
   with self.assertRaises(ValueError):Runner().start(req,s['now'],s['timeout'])
   with self.assertRaises(ValueError):envelope(req,{})
 # Verify discovery duplicate and context isolation
 def test_discovery_duplicate_and_context_isolation(self):
  s=SPEC['discovery'];r=Runner();req=Request(s['context'],s['channel'],s['first'],s['operation'])
  other=r.start(Request('other-context',s['channel'],s['first'],s['operation']),s['now'],s['timeout'])
  old=r.start(req,s['now'],s['timeout']);current=r.start(req,s['now'],s['timeout'])
  self.assertTrue(old.cancel.is_set());self.assertFalse(other.cancel.is_set())
  self.assertIsNone(r.complete(old,{},'',s['now']))
  self.assertTrue(r.cancel('other-context',s['channel'],s['first']));self.assertTrue(other.cancel.is_set());self.assertFalse(current.cancel.is_set())
 # Verify shared discovery
 def test_shared_discovery(self):
  s=SPEC['discovery'];r=Runner();req=Request(s['context'],s['channel'],s['first'],s['operation']);a=r.start(req,s['now'],s['timeout']);req=Request(s['context'],s['channel'],s['second'],s['operation']);b=r.start(req,s['now']+1,s['timeout']);self.assertTrue(a.cancel.is_set());self.assertFalse(r.cancel(s['context'],s['channel'],s['first']));self.assertIsNone(r.complete(a,{},'',s['now']+2));reply=r.complete(b,{'request_id':'spoof','op':'spoof','devices':['one']},'',s['now']+2);self.assertEqual(reply.payload['request_id'],s['second']);self.assertEqual(reply.payload['op'],s['operation']);self.assertTrue(reply.payload['available']);t=r.start(req,s['now'],s['timeout']);self.assertEqual(r.next_deadline(),s['now']+s['timeout']);self.assertEqual(r.expire(s['now']+s['timeout'])[0].payload['error'],'timeout');self.assertTrue(t.cancel.is_set());self.assertEqual(envelope(req,{'data':'x'*MAX_RESPONSE_BYTES}).payload['error'],'response_too_large')
  for i in range(MAX_PENDING):r.start(Request(str(i),'devices','id','devices'),0,1)
  with self.assertRaises(RuntimeError):r.start(Request('overflow','devices','id','devices'),0,1)
  r.clear('0');r.start(Request('overflow','devices','id','devices'),0,1);r.close()
  with self.assertRaises(ValueError):r.start(req,0,1)
 # Verify shared thumbnail helpers
 def test_shared_thumbnail_helpers(self):
  s=SPEC['thumbnails'];self.assertEqual(image.MAX_INPUT_BYTES,s['max_input_bytes']);self.assertEqual(image.MAX_SOURCE_PIXELS,s['max_source_pixels'])
  for c in s['fit']:self.assertEqual(list(image.fit(*c['source'],*c['box'],limits=image.Limits(max_source_pixels=c.get('pixels',image.MAX_SOURCE_PIXELS)))),c['output'])
  with self.assertRaises(ValueError):image.fit(8000,8000,320,320)
  source=image.Thumbnail(3,2,bytes([12,34,56,128])+bytes(20));encoded=image.png(source)
  with self.assertRaises(ValueError):image.decode(encoded,3,2,limits=image.Limits(max_input_bytes=len(encoded)-1))
  with self.assertRaises(ValueError):image.decode(encoded,3,2,limits=image.Limits(max_source_pixels=5))
  rendered=image.render(encoded,320,320,limits=image.Limits(len(encoded),6));self.assertEqual((rendered.width,rendered.height),(3,2));self.assertEqual(image.decode(base64.b64decode(rendered.data_uri.split(',')[1]),3,2).data,source.data)
  with self.assertRaises(ValueError):image.Limits(max_source_pixels=0)

 # Verify shared cache persistence
 def test_shared_cache_persistence(self):
  from test_managedstate import Backend
  from stream_dock_manager_sdk.cache import Persistence
  from stream_dock_manager_sdk.hoststorage import RPCError
  for recovery in ['applied','not-applied']:
   backend=Backend(recovery);backend.records={};c=Cache(100,100000);store=Persistence(backend);store.load(c,0)
   for i in range(70):c.put(str(i),i,16,0,0)
   with self.assertRaises(RPCError):store.flush(c,0)
   c.put('late',99,16,0,0);store.flush(c,0);self.assertEqual(len(backend.records),71);self.assertTrue(all(len(v['mutations'])<=32 for v in backend.commits))
   if recovery=='not-applied':self.assertEqual(backend.commits[0]['id'],backend.commits[1]['id'])
   restored=Cache(100,100000);Persistence(backend).load(restored,0);self.assertEqual(restored.get('late',0),99)
   c.put('late',100,16,0,0);backend.revision+=1
   with self.assertRaises(RPCError):store.flush(c,0)
   attempts=len(backend.commits)
   with self.assertRaises(RPCError):store.flush(c,0)
   self.assertEqual(len(backend.commits),attempts)

 # Verify cache batches by wire bytes
 def test_cache_batches_by_wire_bytes(self):
  from test_managedstate import Backend
  from stream_dock_manager_sdk.cache import Persistence
  backend=Backend('applied');backend.records={};backend.fail=False;c=Cache(10,1<<20);store=Persistence(backend);store.load(c,0)
  for i in range(3):c.put(str(i),'x'*210000,220000,0,0)
  store.flush(c,0);self.assertEqual(len(backend.commits),3);self.assertTrue(all(len(json.dumps(v['mutations']).encode())<512<<10 for v in backend.commits))
