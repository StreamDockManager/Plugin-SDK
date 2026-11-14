"""Manager-owned display themes."""
import re
from typing import Literal, TypedDict

THEME_PROTOCOL = 'plugin-theme-v1'
THEME_EVENT = 'didReceivePluginTheme'
COLORS = ('surface', 'ink', 'muted', 'accent', 'meter', 'border')


class ThemeMedia(TypedDict):
    kind: Literal['media']
    asset_id: str
    format: str
    animated: bool


class ThemeColors(TypedDict):
    surface: str
    ink: str
    muted: str
    accent: str
    meter: str
    border: str
    wallpaper: str | ThemeMedia


class PluginTheme(TypedDict):
    version: Literal[1]
    resolved: Literal['light', 'dark']
    revision: int
    theme: ThemeColors


# Decode plugin theme
def decode_plugin_theme(value: object) -> PluginTheme:
    if (not isinstance(value, dict) or type(value.get('version')) is not int
            or value['version'] != 1 or value.get('resolved') not in ('light', 'dark')
            or type(value.get('revision')) is not int or not 0 <= value['revision'] <= 2**53 - 1):
        raise ValueError('invalid plugin theme')
    theme = value.get('theme')
    if not isinstance(theme, dict) or any(
            not isinstance(theme.get(key), str) or not re.fullmatch(r'#[0-9a-fA-F]{6}', theme[key])
            for key in COLORS):
        raise ValueError('invalid plugin theme colors')
    wallpaper = theme.get('wallpaper')
    if isinstance(wallpaper, str) and 0 < len(wallpaper.encode('utf-8')) <= 64 * 1024:
        return value
    if (isinstance(wallpaper, dict) and wallpaper.get('kind') == 'media'
            and all(isinstance(wallpaper.get(key), str) and wallpaper[key] for key in ('asset_id', 'format'))
            and type(wallpaper.get('animated')) is bool):
        return value
    raise ValueError('invalid plugin theme wallpaper')
