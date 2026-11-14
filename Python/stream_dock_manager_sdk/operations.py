"""Owner-serialized external operation history; never submits or retries work."""
from dataclasses import dataclass,replace
@dataclass(frozen=True)
class Record:
    id:str
    group:str
    sequence:int
    state:str
    pinned:bool
    value:object
    cost:int
    # Expose the charge value from record
    @property
    def charge(self):return len(self.id.encode())+len(self.group.encode())+self.cost+64
class Ledger:
    # Initialize bounded operation records and their sequence counter
    def __init__(self,capacity,limit):
        if type(capacity) is not int or not 0<capacity<=65536 or type(limit) is not int or not 0<limit<=64<<20:raise ValueError('invalid operation history limits')
        self.capacity,self.limit=capacity,limit;self._records={};self.bytes=0;self._sequence=0
    # Return the number of retained ledger entries
    def __len__(self):return len(self._records)
    # Look up a retained operation record by its identifier
    def get(self,id):return self._records.get(id)
    # Return records for ledger
    def records(self,group=''):return sorted((r for r in self._records.values() if not group or r.group==group),key=lambda r:r.sequence)
    # Reserve bounded capacity before admitting new work
    def reserve(self,group,value,cost):
        if not isinstance(group,str) or not group or len(group.encode())>4096 or type(cost) is not int or not 0<=cost<=self.limit or len(self)>=self.capacity or self._sequence>=9007199254740991:raise ValueError('invalid operation or history full')
        sequence=self._sequence+1;id=group
        if id in self._records:
            while True:
                id=group+'#'+str(sequence)
                if id not in self._records:break
                sequence+=1
                if sequence>9007199254740991:raise ValueError('operation sequence exhausted')
        record=Record(id,group,sequence,'pending',False,value,cost)
        if record.charge>self.limit-self.bytes:raise ValueError('operation history byte budget exceeded')
        self._sequence=sequence;self._records[id]=record;self.bytes+=record.charge;return record
    # Update ledger data
    def update(self,id,state,value,cost,pinned=False):
        prior=self._records.get(id)
        if prior is None or state not in ('pending','confirmed','failed','uncertain') or type(cost) is not int or not 0<=cost<=self.limit or cost-prior.cost>self.limit-self.bytes or type(pinned) is not bool:raise ValueError('invalid operation update or history full')
        self._records[id]=replace(prior,state=state,value=value,cost=cost,pinned=pinned);self.bytes+=cost-prior.cost
    # Retire an obsolete generation while retaining its active capacity accounting
    def retire(self,active):
        out=[]
        for r in self.records():
            if r.state in ('confirmed','failed') and not r.pinned and r.group not in active:del self._records[r.id];self.bytes-=r.charge;out.append(r.id)
        return out
