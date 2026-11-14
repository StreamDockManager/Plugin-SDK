use base64::Engine;
use stream_dock_manager_plugin_sdk::{image_data_uri, MAX_PORTABLE_IMAGE_BYTES};
#[test]
// Verify portable image data uri
fn portable_image_data_uri() {
    for mime in ["image/png", "image/svg+xml"] {
        let data = [0, 1, 254, 255];
        assert_eq!(
            image_data_uri(mime, &data).unwrap(),
            format!("data:{mime};base64,AAH+/w==")
        );
        assert_eq!(data, [0, 1, 254, 255]);
        let maximum = vec![0; MAX_PORTABLE_IMAGE_BYTES];
        let encoded = image_data_uri(mime, &maximum).unwrap();
        assert_eq!(
            base64::engine::general_purpose::STANDARD
                .decode(encoded.split_once(',').unwrap().1)
                .unwrap(),
            maximum
        );
        assert!(image_data_uri(mime, &[]).is_err());
        assert!(image_data_uri(mime, &vec![0; MAX_PORTABLE_IMAGE_BYTES + 1]).is_err());
    }
    for mime in [
        "image/jpeg",
        "IMAGE/PNG",
        "image/svg+xml;charset=utf-8",
        "file:///tmp/icon.png",
    ] {
        assert!(image_data_uri(mime, &[1]).is_err());
    }
}
