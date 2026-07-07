// +build darwin
#import <Cocoa/Cocoa.h>
#import <string.h>
#import "display_darwin.h"

// Builds the screen list on the main thread. Iterates [NSScreen screens] in the
// SAME order overlay_darwin.m indexes with monitorIndex, so the index we emit is
// exactly the overlay monitorIndex / Position.MonitorIndex. Index 0 is the
// primary (menu-bar) screen that "자동 (주 화면)" maps to.
static char *lt_display_list_on_main(void) {
    NSArray<NSScreen *> *screens = [NSScreen screens];
    if (screens == nil || [screens count] == 0) {
        return NULL;
    }

    NSMutableString *out = [NSMutableString string];
    NSUInteger count = [screens count];
    for (NSUInteger i = 0; i < count; i++) {
        NSScreen *screen = [screens objectAtIndex:i];

        // localizedName is macOS 10.15+. Fall back to a generic label if the
        // running OS/SDK does not expose it.
        NSString *name = nil;
        if ([screen respondsToSelector:@selector(localizedName)]) {
            name = [screen localizedName];
        }
        if (name == nil || [name length] == 0) {
            name = [NSString stringWithFormat:@"Display %lu", (unsigned long)(i + 1)];
        }

        // Strip our field/record delimiters from the name to keep parsing safe.
        name = [name stringByReplacingOccurrencesOfString:@"\t" withString:@" "];
        name = [name stringByReplacingOccurrencesOfString:@"\n" withString:@" "];

        int primary = (i == 0) ? 1 : 0;
        if (i > 0) {
            [out appendString:@"\n"];
        }
        [out appendFormat:@"%lu\t%d\t%@", (unsigned long)i, primary, name];
    }

    const char *utf8 = [out UTF8String];
    if (utf8 == NULL) {
        return NULL;
    }
    return strdup(utf8);
}

char *lt_display_list(void) {
    __block char *result = NULL;
    if ([NSThread isMainThread]) {
        result = lt_display_list_on_main();
    } else {
        dispatch_sync(dispatch_get_main_queue(), ^{
            result = lt_display_list_on_main();
        });
    }
    return result;
}

void lt_display_free(char *s) {
    if (s != NULL) {
        free(s);
    }
}
