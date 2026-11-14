"""Explicit OpenAction package export; does not weaken capability requirements.

Reference: OpenDeck 3ddecc1ce8952a423916cf066724356267b97410,
plugins/manifest.rs and shared.rs; https://openaction.amankhanna.me/manifest.html.
"""
import copy
import hashlib
from pathlib import PurePosixPath
from authoring_common import relative


# Export a package for one built target using OpenAction image references
def export_manifest(manifest, contents, target, *, materialize_aliases=False):
    """Export a package for one built target using OpenAction image references.

    OpenDeck requires OS even for Node scripts and resolves image stems in the
    order SVG, @2x PNG, PNG. Preserve ambiguous assets with verified unique
    copies instead of selecting a different image silently. This leaves the author's source manifest intact.
    """
    if target not in ('mac', 'windows', 'linux'):
        raise ValueError('Unsupported OpenAction package target')
    result = copy.deepcopy(manifest)
    declared = result.get('OS')
    if declared is not None:
        if not isinstance(declared, list) or any(not isinstance(x, dict) for x in declared):
            raise ValueError('OpenAction OS must be a list of platform objects')
        selected = [x for x in declared if x.get('Platform') == target]
        if not selected:
            raise ValueError(f'Manifest does not declare OpenAction target {target}')
        result['OS'] = selected
    else:
        result['OS'] = [{'Platform': target}]

    # Resolve an OpenAction icon stem while preserving selected asset bytes
    def icon(value):
        relative(value)
        if value.endswith('@2x.png'):
            stem = value[:-7]
        elif PurePosixPath(value).suffix.lower() in ('.png', '.svg'):
            stem = value[:-4]
        elif not PurePosixPath(value).suffix:
            stem = value
        else:
            raise ValueError(f'OpenAction requires SVG/PNG icon assets: {value}')
        relative(stem)
        candidates = [stem + suffix for suffix in ('.svg', '@2x.png', '.png')]
        selected = next((name for name in candidates if name in contents), None)
        if selected is None:
            raise ValueError(f'Missing OpenAction icon asset: {value}')
        if value in contents and selected != value:
            # Preserve the explicitly selected raster/vector bytes under a unique stem
            # Source manifests and Manager packages remain unchanged
            alias_stem = 'openaction-assets/' + hashlib.sha256(value.encode('utf-8')).hexdigest()
            suffix = '.svg' if value.endswith('.svg') else '.png'
            alias = alias_stem + suffix
            alternatives = [alias_stem + ext for ext in ('.svg', '@2x.png', '.png')]
            if any(name in contents for name in alternatives if name != alias):
                raise ValueError('OpenAction generated asset namespace collision')
            if not isinstance(contents, dict) or not isinstance(contents[value], bytes):
                raise ValueError('OpenAction asset alias requires verified source bytes')
            if alias in contents:
                if contents[alias] != contents[value]:
                    raise ValueError('OpenAction generated asset differs from its source')
            elif materialize_aliases:
                contents[alias] = contents[value]
            else:
                raise ValueError('Missing OpenAction generated asset: ' + alias)
            return alias_stem
        return stem

    # Convert manifest image fields to validated OpenAction asset stems
    def fields(item, keys):
        for key in keys:
            if item.get(key):
                item[key] = icon(item[key])

    fields(result, ('Icon', 'CategoryIcon'))
    for action in result.get('Actions', []):
        fields(action, ('Icon',))
        if action.get('Encoder'):
            fields(action['Encoder'], ('Icon', 'background', 'Background'))
        for state in action.get('States', []):
            if state.get('Image') != 'actionDefaultImage':
                fields(state, ('Image',))
    return result


# One portable JS bundle, explicitly declaring all three desktop systems
def export_universal_script_manifest(manifest, contents, *, materialize_aliases=False):
    """One portable JS bundle, explicitly declaring all three desktop systems."""
    if any(str(name).lower().endswith(('.node', '.dll', '.so', '.dylib', '.exe')) for name in contents):
        raise ValueError('Universal JavaScript packages cannot contain platform-native libraries or executables')
    entry = manifest.get('CodePath', '')
    if not isinstance(entry, str) or not entry.endswith(('.js', '.mjs', '.cjs')):
        raise ValueError('Universal script export requires a portable JavaScript entry')
    for key in ('CodePathMac', 'CodePathWin', 'CodePathLin', 'CodePathLinux'):
        if key in manifest and manifest[key] != entry:
            raise ValueError('Universal script export cannot select platform-specific entries')
    if manifest.get('CodePaths'):
        raise ValueError('Universal script export cannot select architecture-specific entries')
    variants = [export_manifest(manifest, contents, target, materialize_aliases=materialize_aliases)
                for target in ('mac', 'windows', 'linux')]
    result = variants[0]
    result['OS'] = [variant['OS'][0] for variant in variants]
    return result
