import json,os,queue,subprocess,sys,threading,time,unittest
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];sys.path.insert(0,str(ROOT/'Python'))
from stream_dock_manager_sdk.connectedws import ServiceClient,Rejected,command_error
class Peer:
 # Initialize peer state and dependencies
 def __init__(self,secure=False):
  self.process=subprocess.Popen([os.environ.get('STREAM_DOCK_MANAGER_NODE','node'),str(ROOT/'tests/service-peer.mjs'),*(['--tls'] if secure else [])],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
  ready=queue.Queue();threading.Thread(target=lambda:ready.put(self.process.stdout.readline().strip()),daemon=True).start()
  try:self.endpoint=ready.get(timeout=5)
  except queue.Empty:self.close();raise RuntimeError('peer startup timed out')
  if not self.endpoint:self.close();raise RuntimeError('peer exited before readiness')
 # Stop peer activity and release owned resources
 def close(self):
  self.process.stdin.close()
  try:self.process.wait(timeout=3)
  except subprocess.TimeoutExpired:self.process.kill();self.process.wait()
  self.process.stdout.close()
# Start connectedws data
def start(work):
 result=[]
 # Drive the connectedws execution loop
 def run():
  try:result.append(work())
  except Exception as error:result.append(error)
 thread=threading.Thread(target=run);thread.start();return thread,result
class ServiceSessionTests(unittest.TestCase):
 # Verify shared service sessions
 def test_shared_service_sessions(self):
  peer=Peer();client=ServiceClient();authenticated=[];waiting=threading.Event()
  # Prepare handshake in the service session tests fixture
  def handshake(channel):
   authenticated.append(True);self.assertEqual(channel.receive(),{'hello':True});channel.send({'auth':'fixture'});self.assertEqual(channel.receive(),{'ready':True})
  # Prepare route in the service session tests fixture
  def route(value):
   if value.get('event')=='waiting':waiting.set()
   return value.get('id')
  try:
   session,created=client.connect('same',peer.endpoint,handshake,route);self.assertTrue(created)
   reused,created=client.connect('same',peer.endpoint,handshake,route);self.assertFalse(created);self.assertIs(reused,session);self.assertEqual(len(authenticated),1)
   stopped=threading.Event();stopped.set()
   with self.assertRaises(Exception):client.connect('same',peer.endpoint,handshake,route,cancel=stopped)
   with self.assertRaises(Exception):client.connect('invalid','https://invalid',handshake,route)
   self.assertTrue(session.alive)
   requests=[start(lambda id=id:session.request(id,dict(id=id))) for id in ['one','two']]
   for id,(thread,result) in zip(['one','two'],requests):thread.join(2);self.assertEqual(result,[dict(id=id)])
   cancel=threading.Event();pending=start(lambda:session.request('wait',dict(id='wait'),cancel));self.assertTrue(waiting.wait(2))
   with self.assertRaisesRegex(RuntimeError,'duplicate'):session.request('wait',dict(id='wait'))
   cancel.set();pending[0].join(2);self.assertIsInstance(pending[1][0],Exception);self.assertTrue(session.alive)
   with self.assertRaisesRegex(ValueError,'oversized'):session.request('oversized',dict(id='oversized',value='x'*65536))
   self.assertTrue(session.alive);self.assertEqual(session.request('echo',dict(id='echo')),dict(id='echo'))
   with self.assertRaises(Exception):session.request('large',dict(id='large'))
   self.assertFalse(session.alive);client.connect('again',peer.endpoint,handshake,route);client.close()
   started=threading.Event()
   # Prepare silent in the service session tests fixture
   def silent(channel):started.set();channel.receive()
   opening=start(lambda:client.connect('silent',peer.endpoint+'/silent',silent,route));self.assertTrue(started.wait(2));client.close();opening[0].join(2);self.assertIsInstance(opening[1][0],Exception)
   _,created=client.connect('final',peer.endpoint,handshake,route);self.assertTrue(created);sent,received=client.network.snapshot();self.assertGreater(sent,0);self.assertGreater(received,0)
   with self.assertRaises(ValueError):client.connect('bad',peer.endpoint+'?secret=value',handshake,route)
   rejected=Rejected('command');self.assertIs(command_error(rejected),rejected);self.assertIn('uncertain',str(command_error(RuntimeError('transport'))))
  finally:client.close();peer.close()
 # Verify untrusted tls rejected before authentication
 def test_untrusted_tls_rejected_before_authentication(self):
  peer=Peer(True);client=ServiceClient()
  try:
   with self.assertRaises(Exception):client.connect('tls',peer.endpoint,lambda _:self.fail('untrusted TLS reached authentication'),lambda _:None)
  finally:client.close();peer.close()
