import json,hashlib,unittest,sys
from pathlib import Path
from copy import deepcopy
ROOT=Path(__file__).resolve().parents[2];sys.path.insert(0,str(ROOT/'Python'))
from stream_dock_manager_sdk.managedstate import ManagedState
from stream_dock_manager_sdk.hoststorage import RPCError
SPEC=json.loads((ROOT/'contracts/fixtures/extensions-v1.json').read_text())['managed_state']
# Return key for managedstate
def key(identity):return 'record:'+hashlib.sha256(identity.encode()).hexdigest()
class Backend:
 # Initialize backend state and dependencies
 def __init__(self,recovery):
  self.recovery=recovery;self.revision=0;self.fail=True;self.records={key(identity):dict(identity=identity,value=value) for identity,value in SPEC['initial'].items()};self.receipts={};self.commits=[]
 # Return version for backend
 def version(self):return dict(generation='1',revision=str(self.revision))
 # Return list keys for backend
 def list_keys(self,cursor,cancel):return dict(self.version(),keys=list(self.records))
 # Return the requested backend value
 def get(self,key,cancel):return dict(self.version(),found=key in self.records,value=deepcopy(self.records[key]))
 # Return receipt for backend
 def receipt(self,generation,id,cancel):return self.receipts.get(id,dict(self.version(),operation_id=id,found=False))
 # Commit the proposed state with revision conflict checks
 def commit(self,version,id,mutations,cancel):
  self.commits.append(deepcopy(dict(version=version,id=id,mutations=mutations)))
  if version!=self.version():raise RPCError('conflict','changed')
  if self.fail and self.recovery=='not-applied':self.fail=False;raise RPCError('indeterminate','lost reply')
  for mutation in mutations:
   if mutation['op']=='delete':self.records.pop(mutation['key'],None)
   else:self.records[mutation['key']]=deepcopy(mutation['value'])
  self.revision+=1;receipt=dict(self.version(),operation_id=id,found=True,committed=True);self.receipts[id]=receipt
  if self.fail:self.fail=False;raise RPCError('indeterminate','lost reply')
  return receipt
class ManagedStateTests(unittest.TestCase):
 # Verify shared managed state recovery
 def test_shared_managed_state_recovery(self):
  for recovery in SPEC['recoveries']:
   backend=Backend(recovery);state=ManagedState('private',backend);self.assertEqual(state.load(),SPEC['initial'])
   with self.assertRaises(RPCError):state.save(SPEC['next'])
   self.assertTrue(state.dirty)
   with self.assertRaises(RuntimeError):state.check_pending()
   state.save(SPEC['newer'])
   if recovery=='not-applied':
    self.assertTrue(state.dirty);self.assertEqual(backend.commits[0],backend.commits[1]);state.save(SPEC['newer'])
   self.assertFalse(state.dirty);state.check_pending();self.assertEqual(state.assurance,'committed')
   self.assertEqual(backend.records[key('count')]['value'],SPEC['newer']['count']);self.assertNotIn(key('removed'),backend.records)
   count=len(backend.commits);state.save(SPEC['newer']);self.assertEqual(len(backend.commits),count)
   with self.assertRaises(RPCError):state.save({str(i):i for i in range(33)})
   self.assertEqual(len(backend.commits),count)
 # Verify portable session conflict and restore bounds
 def test_portable_session_conflict_and_restore_bounds(self):
  class Portable:
   settings={'unknown':{'nested':True}};updates=0
   # Read the next portable value or data segment
   def read(self,cancel):return dict(settings=deepcopy(self.settings),assurance='observed')
   # Update portable data
   def update(self,changes,cancel):self.updates+=1;self.settings.update(changes);return dict(assurance='sent')
  backend=Portable();state=ManagedState('portable',backend,'saved');state.load();state.save(SPEC['next']);state.save(SPEC['next'])
  self.assertEqual(backend.updates,1);self.assertEqual(backend.settings['unknown'],{'nested':True});self.assertEqual(state.assurance,'sent')
  state=ManagedState('session');state.load();state.save(SPEC['next']);self.assertEqual(state.load(),SPEC['next'])
  class Conflict(Backend):
   attempts=0
   # Commit the proposed state with revision conflict checks
   def commit(self,*args):self.attempts+=1;raise RPCError('conflict','changed')
  backend=Conflict('applied');state=ManagedState('private',backend);state.load()
  for value in [SPEC['next'],SPEC['newer']]:
   with self.assertRaises(RPCError):state.save(value)
  self.assertEqual(backend.attempts,1)
  class Cycle:
   # Return list keys for cycle
   def list_keys(self,*args):return dict(generation='1',revision='0',keys=[],cursor='same')
  with self.assertRaisesRegex(ValueError,'cursor'):ManagedState('private',Cycle()).load()
