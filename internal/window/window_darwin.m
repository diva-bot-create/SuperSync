#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

extern void ssWillTerminate(void);

@interface SSDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate, WKUIDelegate, WKNavigationDelegate>
@property(strong) NSWindow *window;
@property(strong) WKWebView *web;
@property(strong) NSURL *home;
@end

static SSDelegate *gDelegate;

@implementation SSDelegate
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)app {
  return YES;
}
- (void)applicationWillTerminate:(NSNotification *)n {
  ssWillTerminate();
}
- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
  [self.window makeKeyAndOrderFront:nil];
  return YES;
}

// Links to anywhere but SuperSync itself open in the default browser.
- (BOOL)isExternal:(NSURL *)u {
  if (!u) return NO;
  if (![u.scheme isEqualToString:@"http"] && ![u.scheme isEqualToString:@"https"] && ![u.scheme isEqualToString:@"mailto"]) return NO;
  return !([u.host isEqualToString:self.home.host] && [u.port isEqual:self.home.port]);
}
- (void)webView:(WKWebView *)wv
    decidePolicyForNavigationAction:(WKNavigationAction *)action
                    decisionHandler:(void (^)(WKNavigationActionPolicy))handler {
  NSURL *u = action.request.URL;
  if ([self isExternal:u]) {
    [[NSWorkspace sharedWorkspace] openURL:u];
    handler(WKNavigationActionPolicyCancel);
    return;
  }
  handler(WKNavigationActionPolicyAllow);
}
- (WKWebView *)webView:(WKWebView *)wv
    createWebViewWithConfiguration:(WKWebViewConfiguration *)cfg
               forNavigationAction:(WKNavigationAction *)action
                    windowFeatures:(WKWindowFeatures *)features {
  NSURL *u = action.request.URL;
  if (u) [[NSWorkspace sharedWorkspace] openURL:u];
  return nil;
}
- (void)webView:(WKWebView *)wv
    runJavaScriptAlertPanelWithMessage:(NSString *)message
                      initiatedByFrame:(WKFrameInfo *)frame
                     completionHandler:(void (^)(void))handler {
  NSAlert *a = [NSAlert new];
  a.messageText = message;
  [a beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) { handler(); }];
}
- (void)webView:(WKWebView *)wv
    runJavaScriptConfirmPanelWithMessage:(NSString *)message
                        initiatedByFrame:(WKFrameInfo *)frame
                       completionHandler:(void (^)(BOOL))handler {
  NSAlert *a = [NSAlert new];
  a.messageText = message;
  [a addButtonWithTitle:@"OK"];
  [a addButtonWithTitle:@"Cancel"];
  [a beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) { handler(r == NSAlertFirstButtonReturn); }];
}
- (void)showSettings:(id)sender {
  [self.web evaluateJavaScript:@"window.selectTab && selectTab('settings')" completionHandler:nil];
}
- (void)reloadPage:(id)sender {
  [self.web reload];
}
@end

static NSMenuItem *item(NSString *title, SEL action, NSString *key, NSEventModifierFlags mods, id target) {
  NSMenuItem *m = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
  if (mods) m.keyEquivalentModifierMask = mods;
  if (target) m.target = target;
  return m;
}

static void buildMenu(NSString *name) {
  NSMenu *bar = [NSMenu new];
  NSEventModifierFlags cmd = NSEventModifierFlagCommand;

  NSMenu *app = [[NSMenu alloc] initWithTitle:name];
  [app addItem:item([@"About " stringByAppendingString:name], @selector(orderFrontStandardAboutPanel:), @"", 0, nil)];
  [app addItem:[NSMenuItem separatorItem]];
  [app addItem:item(@"Settings…", @selector(showSettings:), @",", cmd, gDelegate)];
  [app addItem:[NSMenuItem separatorItem]];
  [app addItem:item([@"Hide " stringByAppendingString:name], @selector(hide:), @"h", cmd, nil)];
  [app addItem:item(@"Hide Others", @selector(hideOtherApplications:), @"h", cmd | NSEventModifierFlagOption, nil)];
  [app addItem:item(@"Show All", @selector(unhideAllApplications:), @"", 0, nil)];
  [app addItem:[NSMenuItem separatorItem]];
  [app addItem:item([@"Quit " stringByAppendingString:name], @selector(terminate:), @"q", cmd, nil)];

  NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
  [edit addItem:item(@"Undo", @selector(undo:), @"z", cmd, nil)];
  [edit addItem:item(@"Redo", @selector(redo:), @"z", cmd | NSEventModifierFlagShift, nil)];
  [edit addItem:[NSMenuItem separatorItem]];
  [edit addItem:item(@"Cut", @selector(cut:), @"x", cmd, nil)];
  [edit addItem:item(@"Copy", @selector(copy:), @"c", cmd, nil)];
  [edit addItem:item(@"Paste", @selector(paste:), @"v", cmd, nil)];
  [edit addItem:item(@"Select All", @selector(selectAll:), @"a", cmd, nil)];

  NSMenu *view = [[NSMenu alloc] initWithTitle:@"View"];
  [view addItem:item(@"Reload", @selector(reloadPage:), @"r", cmd, gDelegate)];
  [view addItem:item(@"Enter Full Screen", @selector(toggleFullScreen:), @"f", cmd | NSEventModifierFlagControl, nil)];

  NSMenu *win = [[NSMenu alloc] initWithTitle:@"Window"];
  [win addItem:item(@"Minimize", @selector(performMiniaturize:), @"m", cmd, nil)];
  [win addItem:item(@"Zoom", @selector(performZoom:), @"", 0, nil)];

  for (NSMenu *m in @[ app, edit, view, win ]) {
    NSMenuItem *holder = [NSMenuItem new];
    holder.submenu = m;
    [bar addItem:holder];
  }
  NSApp.mainMenu = bar;
  NSApp.windowsMenu = win;
}

void ssRun(const char *url, const char *title, int w, int h) {
  @autoreleasepool {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    gDelegate = [SSDelegate new];
    NSApp.delegate = gDelegate;
    NSApp.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
    NSString *name = [NSString stringWithUTF8String:title];
    buildMenu(name);

    NSRect frame = NSMakeRect(0, 0, w, h);
    NSWindow *win = [[NSWindow alloc]
        initWithContentRect:frame
                  styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable |
                            NSWindowStyleMaskResizable
                    backing:NSBackingStoreBuffered
                      defer:NO];
    win.title = name;
    win.minSize = NSMakeSize(720, 520);
    win.backgroundColor = [NSColor colorWithSRGBRed:15 / 255.0 green:15 / 255.0 blue:16 / 255.0 alpha:1];
    win.releasedWhenClosed = NO;
    win.delegate = gDelegate;
    win.tabbingMode = NSWindowTabbingModeDisallowed;
    if (![win setFrameUsingName:@"SuperSync"]) [win center];
    win.frameAutosaveName = @"SuperSync";

    WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
    cfg.mediaTypesRequiringUserActionForPlayback = WKAudiovisualMediaTypeNone;
    WKWebView *web = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
    [web setValue:@NO forKey:@"drawsBackground"]; // the window's dark background shows while loading
    web.UIDelegate = gDelegate;
    web.navigationDelegate = gDelegate;
    web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    win.contentView = web;

    gDelegate.window = win;
    gDelegate.web = web;
    gDelegate.home = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
    [web loadRequest:[NSURLRequest requestWithURL:gDelegate.home]];
    [win makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
    [NSApp run];
  }
}

void ssFocus(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    [gDelegate.window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
  });
}

void ssClose(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    [NSApp terminate:nil];
  });
}
