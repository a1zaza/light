#!/usr/bin/env python3
"""通过真实 Docker 推送开发镜像，不构建或运行容器，不保存机器人秘密。

先使用独立的临时配置执行 docker --config <目录> login 127.0.0.1:5080，
再运行本工具。目标仅允许本机开发仓库，已有测试标签会被拒绝覆盖。
"""
import argparse
import base64
import hashlib
import json
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

AMD64 = 'docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b'
ARM64 = 'docker.io/library/alpine@sha256:1829893486be257df5049ab3b7fc95afbd0fc09d89803cbc580bda1ba5386b29'
ACCEPT = ', '.join(['application/vnd.oci.image.index.v1+json', 'application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json', 'application/vnd.docker.distribution.manifest.v2+json'])
FIXTURES = [
    {'name': 'demo-alpine', 'platforms': ['amd64'], 'first': 'amd64', 'aliases': ['latest', '3.24']},
    {'name': 'demo/alpine', 'platforms': ['amd64'], 'first': 'dev', 'aliases': ['latest', 'test']},
    {'name': 'demo/base/alpine', 'platforms': ['amd64', 'arm64'], 'first': 'amd64', 'aliases': ['latest', '3.24', 'stable']},
    {'name': 'demo/team/backend/runtime', 'platforms': ['amd64', 'arm64'], 'first': 'amd64', 'aliases': ['latest', 'v1', 'v1.0.0']},
]


def digest(data):
    return 'sha256:' + hashlib.sha256(data).hexdigest()


class Registry:
    def __init__(self, address, auth):
        self.address, self.auth = address, auth
        self.tokens = {}

    def token(self, name):
        if name not in self.tokens:
            query = urllib.parse.urlencode({'service': 'light-registry', 'scope': f'repository:{name}:pull,push'})
            req = urllib.request.Request(f'http://{self.address}/auth/token?{query}', headers={'Authorization': 'Basic ' + self.auth})
            with urllib.request.urlopen(req, timeout=15) as response:
                self.tokens[name] = json.load(response)['token']
        return self.tokens[name]

    def request(self, name, path, method='GET', data=None, media=None):
        headers = {'Authorization': 'Bearer ' + self.token(name), 'Accept': ACCEPT}
        if media:
            headers['Content-Type'] = media
        request = urllib.request.Request(f'http://{self.address}/v2/{name}/{path}', data=data, method=method, headers=headers)
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.read(), response.headers

    def get(self, name, ref):
        return self.request(name, 'manifests/' + ref)

    def put(self, name, tag, raw, media):
        _, headers = self.request(name, 'manifests/' + tag, 'PUT', raw, media)
        if headers.get('Docker-Content-Digest') != digest(raw):
            raise RuntimeError('推送返回的摘要不匹配：' + name + ':' + tag)


def docker(config, *args):
    return subprocess.run(['docker', '--config', config, *args], check=True, text=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--registry', default='127.0.0.1:5080')
    parser.add_argument('--docker-config', required=True, help='已登录机器人的临时 Docker 配置目录')
    parser.add_argument('--name-prefix', default='', help='为仓库名称添加前缀，重复测试时避免覆盖旧标签')
    parser.add_argument('--output', default='docs/dev-image-push-results.json')
    args = parser.parse_args()
    if urllib.parse.urlsplit('http://' + args.registry).hostname not in {'127.0.0.1', 'localhost', '::1'}:
        parser.error('仅允许向本机开发仓库写入')
    if args.name_prefix and not re.fullmatch(r'[a-z0-9]+(?:[._-][a-z0-9]+)*-', args.name_prefix):
        parser.error('仓库前缀须由小写字母、数字及分隔符组成，并以单个短横线结尾')
    fixtures = [{**fixture, 'name': args.name_prefix + fixture['name']} for fixture in FIXTURES]
    with open(Path(args.docker_config) / 'config.json') as stream:
        config = json.load(stream)
    auth = config['auths'][args.registry]['auth']
    # Fail early if this is not a robot credential. Never print the decoded secret.
    username = base64.b64decode(auth).decode().split(':', 1)[0]
    if not username.startswith('robot-'):
        parser.error('请使用机器人凭据')
    registry = Registry(args.registry, auth)
    for fixture in fixtures:
        tags = [fixture['first'], *fixture['aliases']]
        if len(fixture['platforms']) > 1:
            tags.append('arm64')
        for tag in tags:
            try:
                registry.get(fixture['name'], tag)
            except urllib.error.HTTPError as error:
                if error.code == 404:
                    continue
                raise
            raise RuntimeError('已有标签，拒绝覆盖：' + fixture['name'] + ':' + tag)

    results = []
    local_tags = []
    try:
        for fixture in fixtures:
            name = fixture['name']
            children = []
            for arch in fixture['platforms']:
                tag = fixture['first'] if arch == 'amd64' else 'arm64'
                source = AMD64 if arch == 'amd64' else ARM64
                destination = f'{args.registry}/{name}:{tag}'
                docker(args.docker_config, 'tag', source, destination)
                local_tags.append(destination)
                docker(args.docker_config, 'push', destination)
                raw, headers = registry.get(name, tag)
                manifest = json.loads(raw)
                config_raw, _ = registry.request(name, 'blobs/' + manifest['config']['digest'])
                image_config = json.loads(config_raw)
                if image_config['architecture'] != arch or image_config['os'] != 'linux':
                    raise RuntimeError('平台配置不匹配：' + destination)
                platform = {'architecture': arch, 'os': 'linux'}
                if arch == 'arm64':
                    platform['variant'] = 'v8'
                children.append({'mediaType': headers['Content-Type'].split(';')[0], 'digest': digest(raw), 'size': len(raw), 'platform': platform})
            if len(children) > 1:
                media = 'application/vnd.oci.image.index.v1+json'
                raw = json.dumps({'schemaVersion': 2, 'mediaType': media, 'manifests': children}, separators=(',', ':')).encode()
            else:
                raw, headers = registry.get(name, fixture['first'])
                media = headers['Content-Type'].split(';')[0]
            for tag in fixture['aliases']:
                registry.put(name, tag, raw, media)
            tags_raw, _ = registry.request(name, 'tags/list')
            tags = sorted(json.loads(tags_raw)['tags'])
            for tag in fixture['aliases']:
                found, _ = registry.get(name, tag)
                if digest(found) != digest(raw):
                    raise RuntimeError('标签摘要不一致：' + name + ':' + tag)
            results.append({'repository': name, 'depth': len(name.split('/')), 'tags': tags, 'platforms': [child['platform'] for child in children], 'aliasDigest': digest(raw), 'children': children})
            print('已核对仓库：', name, '标签：', ', '.join(tags), flush=True)
        output = {'registry': args.registry, 'source': 'Docker Official Image alpine:3.24', 'repositories': results, 'totalTags': sum(len(repo['tags']) for repo in results)}
        Path(args.output).parent.mkdir(parents=True, exist_ok=True)
        Path(args.output).write_text(json.dumps(output, ensure_ascii=False, indent=2) + '\n')
        print(json.dumps(output, ensure_ascii=False, indent=2))
    finally:
        # Only remove local tags created by this invocation. Original source images remain.
        for tag in local_tags:
            subprocess.run(['docker', '--config', args.docker_config, 'image', 'rm', tag], check=False, capture_output=True)


if __name__ == '__main__':
    main()
