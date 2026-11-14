import json,sys,time,threading,unittest
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];sys.path.insert(0,str(ROOT/'Python'))
from stream_dock_manager_sdk.requests import RequestGroup
SPEC=json.loads((ROOT/'contracts/fixtures/extensions-v1.json').read_text())['shared_reads']
# Wait for for
def wait_for(check):
 deadline=time.monotonic()+2
 while not check():
  if time.monotonic()>deadline:raise AssertionError('condition did not complete')
  time.sleep(.001)
# Start requests data
def start(work):
 outcome=[]
 # Drive the requests execution loop
 def run():
  try:outcome.append(work())
  except Exception as error:outcome.append(error)
 thread=threading.Thread(target=run);thread.start();return thread,outcome
class RequestTests(unittest.TestCase):
 # Verify shared reads cancellation cooldown and retirement
 def test_shared_reads_cancellation_cooldown_and_retirement(self):
  group=RequestGroup();release=threading.Event();seen=[];cancel=threading.Event()
  # Execute the queued request tests operation
  def work(signal):seen.append(signal);release.wait(2);return SPEC['value']
  first=start(lambda:group.get(SPEC['key'],work,cancel));second=start(lambda:group.get(SPEC['key'],work))
  wait_for(lambda:SPEC['key'] in group._flights and group._flights[SPEC['key']]['users']==2)
  cancel.set();first[0].join(2);self.assertIsInstance(first[1][0],Exception);self.assertFalse(seen[0].is_set());release.set();second[0].join(2);self.assertEqual(second[1],[SPEC['value']]);self.assertEqual(len(seen),1)
  # Report the pending request tests operation as failed
  def fail(_):raise ValueError('service cooldown')
  with self.assertRaises(ValueError):group.get('cooldown',fail,retry_after=lambda error:SPEC['cooldown_ms'])
  with self.assertRaises(ValueError):group.get('cooldown',lambda _:self.fail('ignored cooldown'))
  with self.assertRaises(ValueError):group.get('expiry',fail,retry_after=lambda _:20)
  time.sleep(.03);self.assertEqual(group.get('expiry',lambda _:7),7)
  old_release=threading.Event();old_started=threading.Event();cancel=threading.Event()
  # Prepare old work in the request tests fixture
  def old_work(_):old_started.set();old_release.wait(2);raise ValueError('late cooldown')
  old=start(lambda:group.get('retired',old_work,cancel,lambda _:SPEC['cooldown_ms']));self.assertTrue(old_started.wait(2));cancel.set();old[0].join(2)
  self.assertEqual(group.get('retired',lambda _:SPEC['value']),SPEC['value']);old_release.set();wait_for(lambda:group._slots==0)
  self.assertEqual(group.get('retired',lambda _:SPEC['value']),SPEC['value'])
  with self.assertRaisesRegex(ValueError,'1 MiB'):group.get('oversized',lambda _:'x'*((1<<20)+1))
  group.close()
  with self.assertRaises(RuntimeError):group.get('closed',work)
 # Verify retired work retains capacity until finished
 def test_retired_work_retains_capacity_until_finished(self):
  group=RequestGroup();release=threading.Event();pending=[]
  for i in range(SPEC['capacity']):
   cancel=threading.Event();thread,outcome=start(lambda i=i,cancel=cancel:group.get(str(i),lambda _:(release.wait(3),1)[1],cancel));pending.append((cancel,thread,outcome))
  wait_for(lambda:len(group._flights)==SPEC['capacity'])
  for cancel,thread,outcome in pending:cancel.set();thread.join(2);self.assertIsInstance(outcome[0],Exception)
  with self.assertRaisesRegex(RuntimeError,'capacity'):group.get('overflow',lambda _:1)
  release.set();wait_for(lambda:group._slots==0);self.assertEqual(group.get('reused',lambda _:1),1);group.close()
