"""Focused omission proofs for the current plan record additions (#180-182)."""
import copy
import unittest
from test_contract import schemas, vectors, validator, problems

class TaskPlanContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.documents=schemas()
        cls.rows={name:value for name,value in vectors().items() if name.startswith('plan/')}

    def test_shared_vectors_validate(self):
        for name,vector in self.rows.items():
            self.assertEqual(problems(validator(self.documents,vector['schema']),vector['items']),[],name)

    def test_every_new_member_and_enum_is_carried(self):
        targets={
            'checkpoint.json':['taskStep','planChange','planRevision','planState','stepEvidence','planOrigin','acceptedPlan','planDeparture','planDepartureRecord','planRefusal','pendingDeparture','planDecision'],
            'run-event.json':['plan_revised','plan_departed','plan_completion_refused','plan_drift_decided'],
        }
        def paths(node,path):
            if isinstance(node,dict):
                for key,value in node.items():
                    if key in ('if','then','else','not','const','default','examples'): continue
                    if key=='properties':
                        for member in value:yield path+[key,member],None
                    if key=='enum':
                        for enum in value:yield path,enum
                    elif key!='enum':yield from paths(value,path+[key])
            elif isinstance(node,list):
                for index,value in enumerate(node):yield from paths(value,path+[index])
        checks=[]
        for name,defs in targets.items():
            for definition in defs:checks.extend((name,p,e) for p,e in paths(self.documents[name]['$defs'][definition],['$defs',definition]))
        checks += [('checkpoint.json',['properties','planState'],None),('tool-offer.json',['$defs','reservedName'],'complete_plan_step')]
        checks += [('run-event.json',['$defs','approval_requested','properties',key],None) for key in ('purpose','plan')]
        checks += [('run-event.json',['$defs','approval_requested','properties','purpose'],value) for value in ('tool-call','plan-drift')]
        for name,path,enum in checks:
            changed=copy.deepcopy(self.documents);node=changed[name]
            for bit in path[:-1]:node=node[bit]
            if enum is None:node[path[-1]]=False
            else:node[path[-1]]['enum']=[value for value in node[path[-1]]['enum'] if value!=enum]
            self.assertTrue(any(problems(validator(changed,v['schema']),v['items']) for v in self.rows.values()),f'uncarried {name} {path} {enum}')
        self.assertGreater(len(checks),100)
