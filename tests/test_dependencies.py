"""Handwritten resolved graph, native closure, metadata and actual generator CLI fixtures."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
HELPER = Path(os.environ.get('DEPENDENCIES_HELPER', ROOT / 'scripts/gen-dependencies.py'))
SPEC = importlib.util.spec_from_file_location('dependencies', HELPER)
d = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(d)


class DependenciesTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='dependencies-fixture-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for folder in ['go', 'scripts', 'docs', 'bin']:
            (self.root / folder).mkdir()
        self.modules = [{'Path':'example.test/framework','Version':'v1.0.0'},
                        {'Path':'example.test/platform','Version':'v2.0.0','Indirect':True}]
        self.packages = [{'ImportPath':'example.test/framework/agent','Module':self.modules[0]}]
        self.lock = {'lockfileVersion':3,'packages':{
            '':{'dependencies':{'same':'1.0.0'}},
            'node_modules/same':{'version':'1.0.0','integrity':'sha512-one','license':'MIT'},
            'node_modules/parent/node_modules/same':{'version':'2.0.0','integrity':'sha512-two','dev':True,'optional':True,'peer':True,'os':['linux']},
            'node_modules/workspace':{'link':True,'resolved':'packages/harness'},
            'packages/harness':{'name':'first-party','version':'1.0.0'}}}
        self.config = {'goos':'linux','goarch':'amd64','cgo':'1','tags':[],'toolchain':'go1.26.10','roles':[]}
        self.write('go/go.mod', 'module fixture\n')
        self.write('go/go.sum', 'example.test/framework v1.0.0 h1:framework\nexample.test/platform v2.0.0 h1:platform\n')
        self.metadata = {'version':1,'go':{
            'example.test/framework@v1.0.0':{'binding':{'path':'example.test/framework','version':'v1.0.0','sum':'h1:framework','go_mod_sum':None},'license':'Apache-2.0','publishing_owner':'unknown: no publishing evidence','repository':'https://example.test/framework'},
            'example.test/platform@v2.0.0':{'binding':{'path':'example.test/platform','version':'v2.0.0','sum':'h1:platform','go_mod_sum':None},'license':'unknown: no license file','publishing_owner':'unknown: no publishing evidence','repository':'unknown: no origin'}},'npm':{
            'node_modules/same':{'binding':{'version':'1.0.0','integrity':'sha512-one'},'license':'MIT','publishing_owner':'unknown: no publishing evidence','repository':'https://example.test/same'},
            'node_modules/parent/node_modules/same':{'binding':{'version':'2.0.0','integrity':'sha512-two'},'license':'unknown: absent package manifest','publishing_owner':'unknown: no publishing evidence','repository':'unknown: no origin'}}}
        self.save()

    def write(self, name, value):
        (self.root / name).write_text(value)

    def save(self):
        for name, value in [('package-lock.json',self.lock),('scripts/dependencies-config.json',self.config),('scripts/dependencies-metadata.json',self.metadata)]:
            self.write(name,json.dumps(value))

    def render(self):
        return d.render(self.root,self.modules,self.packages,self.lock,self.metadata,self.config)

    def fake_go(self, failure=False):
        script = '#!/usr/bin/env python3\nimport json,sys\n'
        if failure:
            script += 'print("fixture-go-detail",file=sys.stderr)\nraise SystemExit(7)\n'
        else:
            script += "args=sys.argv[1:]\n"
            script += "if args==['env','GOVERSION']: print('go1.26.10')\n"
            script += "elif args==['list','-m','-json','all']: print(%r)\n" % '\n'.join(json.dumps(m) for m in self.modules)
            script += "elif args==['list','-deps','-json','-tags=','./...']: print(%r)\n" % '\n'.join(json.dumps(p) for p in self.packages)
            script += "else: raise SystemExit('unexpected fixture command '+repr(args))\n"
        self.write('bin/go', script)
        (self.root/'bin/go').chmod(0o755)
        return dict(os.environ,PATH=str(self.root/'bin')+os.pathsep+os.environ['PATH'])

    def test_unsupported_lock_shape_refused(self):
        self.lock['lockfileVersion']=2
        with self.assertRaisesRegex(ValueError,'requires npm lockfileVersion3'):
            d.npm_entries(self.lock)

    def test_unconfigured_go_toolchain_refused(self):
        env=self.fake_go()
        p=self.root/'bin/go';p.write_text(p.read_text().replace("go1.26.10","go1.26.9"))
        from unittest.mock import patch
        with patch.dict(os.environ,env):
            with self.assertRaisesRegex(ValueError,'inventory requires go1.26.10'):
                d.resolve(self.root,self.config)

    def test_incomplete_native_closure_refused(self):
        self.packages[0]['Incomplete']=True
        from unittest.mock import patch
        with patch.dict(os.environ,self.fake_go()):
            with self.assertRaisesRegex(ValueError,'package closure incomplete'):
                d.resolve(self.root,self.config)

    def test_extra_metadata_cannot_hide_dependency_set_drift(self):
        self.metadata['npm']['node_modules/ghost']=copy.deepcopy(self.metadata['npm']['node_modules/same'])
        with self.assertRaisesRegex(ValueError,'metadata dependency set stale'):
            self.render()

    def test_local_manifest_license_disagreement_refused(self):
        path=self.root/'node_modules/same';path.mkdir(parents=True)
        (path/'package.json').write_text(json.dumps({'name':'same','version':'1.0.0','license':'Apache-2.0'}))
        with self.assertRaisesRegex(ValueError,'lock/package license differs'):
            d.refresh(self.root,self.modules,self.lock)

    def test_exact_graph_and_closure_are_distinct(self):
        body = self.render()
        self.assertIn('example.test/platform | v2.0.0 | indirect | none | module graph only',body)
        self.assertIn('example.test/framework | v1.0.0 | direct | none | in configured package closure (1 packages)',body)
        self.assertIn('`example.test/framework/agent`',body)

    def test_duplicate_npm_locations_versions_and_flags_preserved(self):
        body = self.render()
        self.assertIn('node_modules/same | 1.0.0 | runtime graph',body)
        self.assertIn('node_modules/parent/node_modules/same | 2.0.0 | development, optional, peer',body)
        self.assertIn('unknown: absent package manifest',body)
        self.assertIn('npm installation entries: 2.',body)
        self.assertNotIn('| node_modules/workspace |',body)

    def test_missing_metadata_never_silently_drops_dependency(self):
        del self.metadata['npm']['node_modules/same']
        with self.assertRaisesRegex(ValueError,'metadata dependency set stale'):
            self.render()

    def test_changed_version_or_integrity_requires_metadata_refresh(self):
        self.lock['packages']['node_modules/same']['version']='1.0.1'
        with self.assertRaisesRegex(ValueError,'metadata version/integrity stale'):
            self.render()

    def test_changed_module_sum_requires_metadata_refresh(self):
        self.write('go/go.sum','example.test/framework v1.0.0 h1:changed\nexample.test/platform v2.0.0 h1:platform\n')
        with self.assertRaisesRegex(ValueError,'metadata version/sum stale'):
            self.render()

    def test_local_replacement_has_no_borrowed_version_metadata(self):
        self.modules[0]['Replace']={'Path':'../local-framework','Dir':'/fixture/local-framework'}
        self.metadata['go']['example.test/framework@v1.0.0']['binding']={'path':'../local-framework','version':None,'sum':None}
        body=self.render()
        self.assertIn('../local-framework',body)
        self.assertNotIn('/fixture/local-framework',body)

    def test_metadata_edit_changes_reproducibility_digest(self):
        before=self.render()
        self.metadata['npm']['node_modules/same']['license']='BSD-3-Clause'
        self.save()
        self.assertNotEqual(before,self.render())

    def test_actual_generator_cli_with_handwritten_go_command_fixture(self):
        result=subprocess.run([sys.executable,str(HELPER),'--root',str(self.root)],env=self.fake_go(),capture_output=True,text=True,timeout=20)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertIn('module graph only',(self.root/'docs/dependencies.md').read_text())

    def test_actual_scratch_check_rejects_dependency_metadata_and_closure_drift(self):
        checker=ROOT/'scripts/dev/generated_docs.py'
        self.write('scripts/gen-dependencies.py', HELPER.read_text())
        self.write('jpdev.json',json.dumps({'version':1,'generators':[{'name':'dependencies','run':'python3 scripts/gen-dependencies.py','outputs':['docs/dependencies.md'],'timeout':10}]}))
        subprocess.run(['git','init','-q',str(self.root)],check=True,capture_output=True)
        env=self.fake_go()
        generate=subprocess.run([sys.executable,str(HELPER),'--root',str(self.root)],env=env,capture_output=True,text=True,timeout=20)
        self.assertEqual(generate.returncode,0,generate.stderr)
        def check():
            return subprocess.run([sys.executable,str(checker),'--root',str(self.root)],env=env,capture_output=True,text=True,timeout=20)
        fresh=check();self.assertEqual(fresh.returncode,0,fresh.stderr)
        self.metadata['npm']['node_modules/same']['license']='BSD-3-Clause';self.save()
        changed=check();self.assertNotEqual(changed.returncode,0);self.assertIn('stale docs/dependencies.md',changed.stderr)
        self.metadata['npm']['node_modules/same']['license']='MIT';self.save()
        self.packages.append({'ImportPath':'example.test/platform/native','Module':self.modules[1]});self.fake_go()
        changed=check();self.assertNotEqual(changed.returncode,0);self.assertIn('stale docs/dependencies.md',changed.stderr)
        self.packages.pop();self.fake_go()
        self.lock['packages']['node_modules/same']['version']='1.0.1';self.save()
        changed=check();self.assertNotEqual(changed.returncode,0);self.assertIn('metadata version/integrity stale',changed.stderr)

    def test_multiple_license_files_remain_unknown(self):
        path=self.root/'versioned-module';path.mkdir()
        (path/'LICENSE-APACHE').write_text('Apache License\nVersion 2.0, January 2004\n')
        (path/'LICENSE-MIT').write_text('Permission is hereby granted, free of charge, to any person obtaining a copy\n')
        self.modules[0]['Dir']=str(path)
        metadata=d.refresh(self.root,self.modules,self.lock)
        record=metadata['go']['example.test/framework@v1.0.0']
        self.assertIn('unknown: multiple versioned license files',record['license'])
        self.assertEqual(len(record['evidence']),2)

    def test_ambiguous_license_text_remains_unknown(self):
        path=self.root/'LICENSE';path.write_text('Apache License\nVersion 2.0, January 2004\nPermission is hereby granted, free of charge, to any person obtaining a copy\n')
        self.assertIn('unknown: multiple license texts',d.license_from_file(path))

    def test_command_status_is_a_direct_refusal(self):
        with self.assertRaisesRegex(ValueError, r'command failed \(7\)'):
            d.run([sys.executable,'-c','print("fixture-output"); raise SystemExit(7)'],self.root,dict(os.environ))

    def test_actual_command_failure_is_explicit_and_writes_no_inventory(self):
        result=subprocess.run([sys.executable,str(HELPER),'--root',str(self.root)],env=self.fake_go(True),capture_output=True,text=True,timeout=20)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('command failed (7): go env GOVERSION',result.stderr)
        self.assertIn('fixture-go-detail',result.stderr)
        self.assertFalse((self.root/'docs/dependencies.md').exists())

    def test_versioned_local_manifest_identity_must_match_lock(self):
        path=self.root/'node_modules/same';path.mkdir(parents=True)
        (path/'package.json').write_text(json.dumps({'name':'same','version':'9.0.0','license':'MIT'}))
        with self.assertRaisesRegex(ValueError,'installed package identity differs from lock'):
            d.refresh(self.root,self.modules,self.lock)

    def test_license_uses_versioned_file_evidence(self):
        path=self.root/'LICENSE';path.write_text('Apache License\nVersion 2.0, January 2004\n')
        self.assertEqual(d.license_from_file(path),'Apache-2.0')
        path.write_text('Unidentified license text')
        self.assertIn('unknown:',d.license_from_file(path))


if __name__ == '__main__':
    unittest.main()
