#!/usr/bin/env python3
"""Exercise the actual speech service contract using an official sample WAV."""
import argparse
import json
import pathlib
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--runtime', type=pathlib.Path, required=True)
    parser.add_argument('--model', type=pathlib.Path, required=True)
    parser.add_argument('--audio', type=pathlib.Path, required=True)
    args = parser.parse_args()
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    prefix = '/' + uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix='praimate-voice-smoke-') as temp:
        log_path = pathlib.Path(temp) / 'server.log'
        with log_path.open('wb') as log:
            proc = subprocess.Popen([
                str(args.runtime.resolve()), '-m', str(args.model.resolve()), '-t', '2',
                '-l', 'auto', '--host', '127.0.0.1', '--port', str(port),
                '--request-path', prefix, '--public', str(pathlib.Path(temp) / 'no-web-ui'),
                '-ng', '-mc', '0',
            ], cwd=args.runtime.resolve().parent, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 120
                while True:
                    if proc.poll() is not None:
                        raise RuntimeError('Whisper exited before becoming ready')
                    try:
                        with socket.create_connection(('127.0.0.1', port), timeout=1):
                            break
                    except OSError:
                        if time.monotonic() > deadline:
                            raise RuntimeError('Whisper readiness timeout')
                        time.sleep(0.2)
                boundary = uuid.uuid4().hex
                body = bytearray()
                for name, value in {'response_format': 'json', 'language': 'auto', 'temperature': '0.0', 'carry_initial_prompt': 'false'}.items():
                    body.extend(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
                body.extend(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="audio.wav"\r\nContent-Type: audio/wav\r\n\r\n'.encode())
                body.extend(args.audio.read_bytes())
                body.extend(f'\r\n--{boundary}--\r\n'.encode())
                request = urllib.request.Request(
                    f'http://127.0.0.1:{port}{prefix}/inference', bytes(body),
                    {'Content-Type': 'multipart/form-data; boundary=' + boundary},
                )
                with urllib.request.urlopen(request, timeout=180) as response:
                    result = json.load(response)
                if 'country' not in result.get('text', '').lower():
                    raise RuntimeError(f'Unexpected transcription: {result}')
                print('Whisper Base: official JFK sample transcription passed')
            except Exception:
                log.flush()
                print(log_path.read_text(errors='replace')[-8000:])
                raise
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()


if __name__ == '__main__':
    main()
