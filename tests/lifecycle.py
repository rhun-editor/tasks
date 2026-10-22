#!/usr/bin/env python3
"""Drive task discovery against protocol 1, negotiated command removal and the tasks feature (the
list published with tasks-set, again when it changes)."""
import os
from pathlib import Path
import queue
import subprocess
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]
# make tasks are argv on Windows (no POSIX shell there), a shell line elsewhere
WINDOWS = os.name == 'nt'
OLD_TASK = (b'make.old argv 8 9 0 14\nmake oldmake\x00old\x00matcher gcc 0\n' if WINDOWS
            else b'make.old shell 8 8 0 14\nmake oldmake oldmatcher gcc 0\n')
NEW_FORM = b'make.new argv' if WINDOWS else b'make.new shell'


class Lifecycle(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix='rhun-tasks-test-')
        cls.binary = Path(cls.tmp.name) / ('tasks.exe' if os.name == 'nt' else 'tasks')
        subprocess.run(['go', 'build', '-o', str(cls.binary), '.'], cwd=ROOT, check=True)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def session(self, commands, tasks=False):
        with tempfile.TemporaryDirectory(prefix='rhun-tasks-project-') as folder:
            project = Path(folder)
            makefile = project / 'Makefile'
            makefile.write_text('old:\n\ttrue\nkeep:\n\ttrue\n')
            proc = subprocess.Popen([str(self.binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE)
            messages = queue.Queue()

            def read():
                while line := proc.stdout.readline():
                    fields = line.decode().strip().split()
                    body = b''
                    if len(fields) > 2 and fields[1] in ('toast', 'status', 'tasks-set'):
                        body = proc.stdout.read(int(fields[-1]))
                    messages.put((fields, body))

            reader = threading.Thread(target=read, daemon=True)
            reader.start()

            def send(line):
                proc.stdin.write((line + '\n').encode())
                proc.stdin.flush()

            def take():
                try:
                    return messages.get(timeout=8)
                except queue.Empty:
                    self.fail('extension did not send the expected request')

            try:
                self.assertEqual(take()[0], ['hello', '1', 'commands', 'terminals', 'tasks'])
                if commands:
                    send('0 features commands' + (' terminals tasks' if tasks else ''))
                send('0 ready ' + str(project))
                if tasks:
                    fields, body = take()
                    self.assertEqual(fields[1], 'tasks-set')
                    self.assertTrue(body.startswith(b'2\n' + OLD_TASK), body)
                    send('ok ' + fields[0])
                initial = []
                for _ in range(2):
                    fields, _ = take()
                    self.assertEqual(fields[1], 'register')
                    initial.append(fields[2])
                    send('ok ' + fields[0])
                self.assertEqual(initial, ['make_old', 'make_keep'])

                makefile.write_text('keep:\n\ttrue\nnew:\n\ttrue\n')
                send('0 run tasks.rescan 0')
                seen = []
                while True:
                    fields, body = take()
                    if fields[1] == 'tasks-set':
                        self.assertIn(NEW_FORM, body)
                        self.assertNotIn(b'make.old', body)
                        seen.append(('tasks-set',))
                        send('ok ' + fields[0])
                        continue
                    if fields[1] == 'toast':
                        self.assertEqual(body, b'2 tasks found')
                        send('ok ' + fields[0])
                        break
                    seen.append(tuple(fields[1:3]))
                    send('ok ' + fields[0])
                expected = [('register', 'make_new')]
                if commands:
                    expected.insert(0, ('unregister', 'make_old'))
                if tasks:
                    expected.insert(0, ('tasks-set',))
                self.assertEqual(seen, expected)
                send('0 bye')
                proc.wait(timeout=5)
                self.assertEqual(proc.returncode, 0, proc.stderr.read().decode())
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()
                proc.stdin.close()
                proc.stdout.close()
                proc.stderr.close()
                reader.join(timeout=2)

    def test_removed_tasks_leave_when_command_removal_is_negotiated(self):
        self.session(commands=True)

    def test_old_hosts_receive_no_unregister_requests(self):
        self.session(commands=False)

    def test_a_tasks_host_gets_the_list_and_its_changes(self):
        self.session(commands=True, tasks=True)


if __name__ == '__main__':
    unittest.main()
