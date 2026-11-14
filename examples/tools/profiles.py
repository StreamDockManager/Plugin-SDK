"""Offline schema-v1 suggested profiles; package generation never edits author sources."""
from authoring_common import add_navigation

from copy import deepcopy
import json
from pathlib import Path
from project import regular, tree_files, relative
ROOT = Path(__file__).resolve().parent

# Load supported hardware models used for suggested profiles
def models():
    document = json.loads(regular(ROOT / 'profile-hardware.v1.json'))
    if document['schema_version'] != 1:
        raise ValueError('Unsupported profile hardware data')
    return document['models']

# Generate profiles
def generate_profiles(manifest):
    owner = manifest['UUID']
    capabilities = manifest.get('StreamDockManager', {}).get('Capabilities', {}).get('actions', {})
    output = {}
    for model in models():
        controls = model['controls']
        compatible = lambda action: [control for control in controls if control['kind'] in capabilities.get(action['UUID'], {}).get('control_types', ['button'])]
        available = [action for action in manifest['Actions'] if compatible(action)]
        unavailable = [action['UUID'] for action in manifest['Actions'] if not compatible(action)]
        buttons = [control for control in controls if control['kind'] == 'button']
        # Allocate the next compatible control in the suggested device layout
        def allocate(navigation):
            reserved = {control['id'] for control in buttons[-2:]} if navigation else set()
            pages = []
            # Create a new suggested-profile page when existing capacity is exhausted
            def page():
                result = dict(id=f'{owner}-{model["model_id"]}-{len(pages)+1}', name=manifest['Name'] + (f' {len(pages)+1}' if pages else ''), bindings={})
                pages.append(result)
                return result
            current = page()
            for action in available:
                choices = lambda: [control for control in compatible(action) if control['id'] not in current['bindings'] and control['id'] not in reserved]
                targets = choices()
                if not targets:
                    if not navigation: return None
                    current = page()
                    targets = choices()
                if not targets: raise ValueError('No place for compatible action ' + action['UUID'])
                capability = capabilities.get(action['UUID'], {})
                preference = ['encoder', 'button', 'touch'] if capability.get('additive_dial') or action['UUID'].rsplit('.', 1)[-1] in ('intensity', 'duration', 'select-shocker') else ['button', 'touch', 'encoder']
                targets.sort(key=lambda control: preference.index(control['kind']))
                assigned = dict(kind='plugin', plugin_id=owner, action_id=action['UUID'], settings={})
                if targets[0]['kind'] == 'encoder':
                    # Manager preserves signed rotation in plugin lanes. Keep the
                    # same plugin settings for left, right, and press inputs
                    assigned = dict(kind='dial', actions=[deepcopy(assigned) for _ in range(3)])
                current['bindings'][targets[0]['id']] = dict(title=action['Name'], color='#00000000', action=assigned)
            add_navigation(pages, buttons)
            return pages
        pages = allocate(False) or allocate(True)
        profile = dict(id=f'{owner}-{model["model_id"]}', name=f'{manifest["Name"]} · {model["name"]}', model_id=model['model_id'], active_page=pages[0]['id'], pages=pages)
        document = dict(schema_version=1, minimum_manager='0.1.0', configuration_revision=0, saved_macros=[], rules=[], history=dict(undo=[], redo=[]), active_profile=profile['id'], profiles=[profile], unavailable_actions=unavailable)
        output[f'profiles/{model["model_id"]}.json'] = (json.dumps(document, indent=4) + '\n').encode()
    return output


# Package profiles
def package_profiles(source, manifest):
    source = Path(source)
    files = {}
    if (source / 'profiles').exists():
        files.update({'profiles/' + name: regular(path) for name, path in tree_files(source / 'profiles').items()})
    if (source / 'profile.json').exists():
        raise ValueError('Use explicit profiles/<model>.json suggestions, not profile.json')
    if not any(name.endswith('.json') and name != 'profiles/asset-aliases.json' for name in files):
        files.update(generate_profiles(manifest))
    validate_profiles(files, manifest)
    return files

# Validate profiles
def validate_profiles(files, manifest):
    hardware = {model['model_id']: model for model in models()}
    actions = {action['UUID'] for action in manifest['Actions']}
    capabilities = manifest.get('StreamDockManager', {}).get('Capabilities', {}).get('actions', {})
    for name, data in files.items():
        if name == 'profile.json': raise ValueError('Root profile.json is unsupported')
        if (not name.startswith('profiles/') or name.count('/') != 1 or not name.endswith('.json') or name.endswith('asset-aliases.json')):
            continue
        document = json.loads(data)
        validate_workspace_envelope(document, name)
        if len(document['profiles']) != 1:
            raise ValueError('A suggested layout needs one schema-v1 profile: ' + name)
        profile = document['profiles'][0]
        if document['active_profile'] != profile['id']:
            raise ValueError('Invalid suggested active profile: ' + name)
        model = hardware.get(profile['model_id'])
        if not model or name != f'profiles/{profile["model_id"]}.json':
            raise ValueError('Suggested profile filename/model mismatch: ' + name)
        controls = {control['id']: control for control in model['controls']}
        pages = {page['id'] for page in profile['pages']}
        if len(pages) != len(profile['pages']) or not pages or profile['active_page'] not in pages:
            raise ValueError('Invalid suggested page identities')
        assets = set()
        if profile.get('background_asset'): assets.add(profile['background_asset'])
        found = set()
        # Check profiles data
        def check(action, control, depth=0):
            if not isinstance(action, dict) or depth > 8:
                raise ValueError('Invalid or too deeply nested suggested action')
            kind = action.get('kind')
            if kind == 'plugin':
                if action.get('plugin_id') != manifest['UUID'] or action.get('action_id') not in actions:
                    raise ValueError('Suggested action belongs to another plugin')
                if control['kind'] not in capabilities.get(action['action_id'], {}).get('control_types', ['button']):
                    raise ValueError('Suggested action uses an incompatible control')
                found.add(action['action_id'])
            elif kind == 'page':
                if action.get('page_id') not in pages:
                    raise ValueError('Suggested navigation target is missing')
            elif kind == 'macro':
                if not isinstance(action.get('actions'), list) or len(action['actions']) > 100:
                    raise ValueError('Invalid suggested macro actions')
                for child in action['actions']: check(child, control, depth + 1)
            elif kind == 'dial':
                if control['kind'] != 'encoder' or not isinstance(action.get('actions'), list) or len(action['actions']) != 3:
                    raise ValueError('Suggested dial needs an encoder and three explicit actions')
                for child in action['actions']: check(child, control, depth + 1)
            elif kind == 'toggle':
                check(action.get('on'), control, depth + 1); check(action.get('off'), control, depth + 1)
            elif kind not in ('none', 'builtin', 'hotkey', 'open_url', 'open_path', 'application', 'media', 'volume', 'mute', 'command', 'profile', 'navigate', 'swipe_navigation', 'delay', 'brightness', 'lighting', 'sleep'):
                raise ValueError('Unsupported suggested action kind')
        for page in profile['pages']:
            for control_id, binding in page['bindings'].items():
                if control_id not in controls:
                    raise ValueError('Unknown suggested control: ' + control_id)
                if binding.get('asset'): assets.add(binding['asset'])
                action = binding.get('action')
                if not isinstance(action, dict):
                    raise ValueError('Invalid suggested action')
                if controls[control_id]['kind'] == 'encoder' and action.get('kind') not in ('none', 'dial'):
                    raise ValueError('Suggested encoder needs explicit left, right and press actions')
                check(action, controls[control_id])
        aliases = json.loads(files.get('profiles/asset-aliases.json', b'{}'))
        for asset in assets:
            import uuid
            if str(uuid.UUID(asset)) != asset: raise ValueError('Invalid suggested asset ID')
            destination = aliases.get(asset)
            if destination:
                relative(destination)
                if not destination.startswith('assets/') or destination.count('/') != 1: raise ValueError('Invalid suggested asset alias')
                candidates = ['profiles/' + destination]
            else: candidates = [f'profiles/assets/{asset}.png', f'profiles/assets/{asset}.sdmedia']
            if not any(candidate in files for candidate in candidates): raise ValueError('Missing suggested profile asset')
        config = document.get('plugin_configuration')
        if config is None: config = {}
        if set(config) - {'settings', 'action_defaults'} or not isinstance(config.get('settings', {}), dict):
            raise ValueError('Invalid suggested configuration')
        if any(action not in actions or not isinstance(value, dict) for action, value in config.get('action_defaults', {}).items()):
            raise ValueError('Invalid suggested action defaults')


# Require current workspace fields; never fill in or migrate author data
def validate_workspace_envelope(document, name):
    """Require current workspace fields; never fill in or migrate author data."""
    if not isinstance(document, dict):
        raise ValueError('Suggested workspace must be an object: ' + name)
    required = ('schema_version', 'configuration_revision', 'saved_macros', 'rules', 'history', 'active_profile', 'profiles')
    for field in required:
        if field not in document:
            raise ValueError(f'Suggested workspace missing required field {field}: {name}')
    if type(document['schema_version']) is not int or document['schema_version'] != 1:
        raise ValueError('A suggested layout needs one schema-v1 profile: ' + name)
    revision = document['configuration_revision']
    if type(revision) is not int or not 0 <= revision <= 2**64 - 1:
        raise ValueError('Invalid suggested workspace configuration_revision: ' + name)
    for field in ('saved_macros', 'rules', 'profiles'):
        if not isinstance(document[field], list):
            raise ValueError(f'Suggested workspace {field} must be an array: {name}')
    if not isinstance(document['active_profile'], str):
        raise ValueError('Suggested workspace active_profile must be a string: ' + name)
    history = document['history']
    if not isinstance(history, dict) or set(history) != {'undo', 'redo'} or any(not isinstance(history[field], list) for field in ('undo', 'redo')):
        raise ValueError('Suggested workspace history needs undo and redo arrays: ' + name)
