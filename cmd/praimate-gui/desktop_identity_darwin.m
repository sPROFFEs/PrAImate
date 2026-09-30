#import <Cocoa/Cocoa.h>

void praimate_set_icon(const void *bytes, int length) {
    // Copy before returning to Go; the main queue must not retain a Go pointer.
    NSData *data = [[NSData alloc] initWithBytes:bytes length:length];
    dispatch_async(dispatch_get_main_queue(), ^{
        NSImage *icon = [[NSImage alloc] initWithData:data];
        if (icon) [NSApp setApplicationIconImage:icon];
        [icon release];
        [data release];
    });
}
