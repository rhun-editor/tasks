#!/usr/bin/env python3
"""Native task-provider protocol lifecycle, with explicit fake-host replies."""
import contextlib
import os
from pathlib import Path
import queue
import subprocess
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]


class Peer:
    def __init__(self, binary, project, tasks=False, commands=True):
        self.proc = subprocess.Popen([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.PIPE, env={**os.environ, "RHUN_PROJECT": str(project)})
        self.messages = queue.Queue()
        def read():
            while line := self.proc.stdout.readline():
                fields = line.decode().rstrip('\n').split(' ')
                body = b''
                if len(fields) > 2 and fields[1] in ('toast', 'status', 'choose', 'term', 'task-run'):
                    body = self.proc.stdout.read(int(fields[-1]))
                self.messages.put((fields, body))
        self.reader = threading.Thread(target=read, daemon=True)
        self.reader.start()
        assert self.take()[0] == ['hello', '1', 'commands', 'tasks']
        features = (['commands'] if commands else []) + (['tasks'] if tasks else [])
        if features:
            self.send('0 features ' + ' '.join(features))
        self.send('0 ready ' + str(project))
    def send(self, line):
        self.proc.stdin.write((line + '\n').encode())
        self.proc.stdin.flush()
    def take(self):
        try:
            return self.messages.get(timeout=8)
        except queue.Empty:
            raise AssertionError('extension did not send expected request')
    def ack(self, fields, suffix=''):
        self.send('ok ' + fields[0] + ((' ' + suffix) if suffix else ''))
        # Model distinct user actions, rather than overlapping native prompts.
        time.sleep(.02)
    def request(self, verb, reply='', args=None):
        fields, body = self.take()
        assert fields[1] == verb, (fields, body)
        if args is not None:
            assert fields[2:] == args, fields
        self.ack(fields, reply)
        return fields, body
    def rescan(self, count):
        self.send('0 run tasks.rescan 0')
        operations = []
        for _ in range(200):
            fields, body = self.take()
            operations.append((fields, body))
            self.ack(fields)
            if fields[1] == 'toast' and body == f'{count} task{"" if count == 1 else "s"} found'.encode():
                return operations
        raise AssertionError('rescan did not finish')
    def close(self):
        if self.proc.poll() is None:
            with contextlib.suppress(BrokenPipeError):
                self.send('0 bye')
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
        self.reader.join(timeout=2)
        error = self.proc.stderr.read()
        for pipe in (self.proc.stdin, self.proc.stdout, self.proc.stderr):
            pipe.close()
        assert self.proc.returncode == 0, error


class ManagedLifecycle(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix='rhun-managed-tasks-')
        cls.binary = Path(cls.tmp.name) / ('tasks.exe' if os.name == 'nt' else 'tasks')
        subprocess.run(['go', 'build', '-o', str(cls.binary), '.'], cwd=ROOT, check=True)
    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()
    def test_run_stop_restart_and_output(self):
        with tempfile.TemporaryDirectory(prefix='rhun-task-project-') as folder:
            project = Path(folder) / "project  space 😀"
            project.mkdir()
            (project / 'Makefile').write_text('build:\n\ttrue\n')
            peer = Peer(self.binary, project, tasks=True)
            try:
                peer.request('register')
                peer.send('0 run tasks.make_build 0')
                fields, body = peer.request('task-run', '101')
                self.assertEqual(fields[3:], [str(len(str(project).encode())), '10', str(len(str(project).encode()) + 20)])
                self.assertEqual(body, str(project).encode() + b'make buildmake build')
                peer.request('task-show', args=['101'])
                peer.send('0 run tasks.stop 0')
                peer.request('task-status', 'running -1 0', ['101'])
                _, body = peer.request('choose', '1')
                self.assertEqual(body, b'Stop a task\nmake build (running)\n')
                peer.request('task-stop', args=['101'])
                peer.send('0 run tasks.restart 0')
                peer.request('task-status', 'exited 0 12', ['101'])
                peer.request('choose', '1')
                fields, _ = peer.request('task-restart', '102')
                self.assertEqual(fields[-1], '101')
                peer.request('task-show', args=['102'])
                peer.send('0 run tasks.output 0')
                peer.request('task-status', 'running -1 0', ['102'])
                peer.request('task-status', 'exited 0 12', ['101'])
                peer.request('choose', '1')
                peer.request('task-show', args=['102'])
            finally:
                peer.close()
    def test_config_last_good_label_refresh_and_project_isolation(self):
        with tempfile.TemporaryDirectory(prefix='rhun-task-config-') as folder:
            project = Path(folder) / 'first'
            (project / '.rhun').mkdir(parents=True)
            config = project / '.rhun/tasks.json'
            config.write_text('{"version":1,"tasks":[{"id":"deploy","label":"Staging","command":"echo staging"}]}')
            peer = Peer(self.binary, project)
            try:
                fields, _ = peer.request('register')
                self.assertEqual(fields[2], 'config_deploy')
                config.write_text('{"version":1,"tasks":[{"id":"deploy","label":"Production","command":"echo production"}]}')
                operations = peer.rescan(1)
                self.assertEqual([f[1] for f, _ in operations], ['unregister', 'register', 'toast'])
                self.assertEqual(operations[1][0][-1], 'Production')
                config.write_text('{"version":1,"version":2,"tasks":[]}')
                operations = peer.rescan(1)
                self.assertTrue(any(b'Cannot load .rhun/tasks.json' in body for _, body in operations))
                self.assertFalse(any(f[1] == 'unregister' for f, _ in operations))
                peer.send('0 run tasks.config_deploy 0')
                _, body = peer.request('term')
                self.assertEqual(body, b'echo production')
                other = Path(folder) / 'other'
                other.mkdir()
                peer.send('0 project ' + str(other))
                peer.request('unregister', args=['config_deploy'])
                operations = peer.rescan(0)
                self.assertFalse(any(f[1] == 'register' for f, _ in operations))
            finally:
                peer.close()
    def test_old_host_changed_label_disables_stale_palette_action(self):
        with tempfile.TemporaryDirectory(prefix='rhun-old-task-config-') as folder:
            project = Path(folder)
            (project / '.rhun').mkdir()
            config = project / '.rhun/tasks.json'
            config.write_text('{"version":1,"tasks":[{"id":"deploy","label":"Staging","command":"echo staging"}]}')
            peer = Peer(self.binary, project, commands=False)
            try:
                peer.request('register')
                config.write_text('{"version":1,"tasks":[{"id":"deploy","label":"Production","command":"echo production"}]}')
                self.assertEqual([f[1] for f, _ in peer.rescan(1)], ['toast'])
                peer.send('0 run tasks.config_deploy 0')
                _, body = peer.request('toast')
                self.assertIn(b'use Run Task', body)
                peer.send('0 run tasks.run 0')
                peer.request('choose', '1')
                _, body = peer.request('term')
                self.assertEqual(body, b'echo production')
            finally:
                peer.close()


if __name__ == '__main__':
    unittest.main()
