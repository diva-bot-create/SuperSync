#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <UserNotifications/UserNotifications.h>

extern void ssWillTerminate(void);
extern void ssMenuAction(int action);

static BOOL gKeepRunning = YES; // closing the window hides it; SuperSync stays in the menu bar

// The page draws its own title bar: this web view remembers the last click,
// so the page can ask to drag the window from its header.
@interface SSWebView : WKWebView
@property(strong) NSEvent *lastDown;
@end
@implementation SSWebView
- (void)mouseDown:(NSEvent *)e {
  self.lastDown = e;
  [super mouseDown:e];
}
- (BOOL)acceptsFirstMouse:(NSEvent *)e {
  return YES; // a click on the header of a background window can drag it
}
@end

static const CGFloat kHeader = 56;  // the page's header height (the title bar)
static const CGFloat kLightsX = 20; // where the window buttons start

@interface SSDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate, WKUIDelegate, WKNavigationDelegate, WKScriptMessageHandler>
@property(strong) NSWindow *window;
@property(strong) SSWebView *web;
@property(strong) NSURL *home;
@property(strong) NSStatusItem *status;
@end

static SSDelegate *gDelegate;

@implementation SSDelegate
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)app {
  return !gKeepRunning;
}
// Closing the window: with "keep running", hide it (and the Dock icon);
// SuperSync carries on from the menu bar.
- (BOOL)windowShouldClose:(NSWindow *)w {
  if (!gKeepRunning) return YES;
  [w orderOut:nil];
  [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
  return NO;
}
- (void)showWindow:(id)sender {
  [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
  [self.window makeKeyAndOrderFront:nil];
  [NSApp activateIgnoringOtherApps:YES];
  [self placeLights];
}
- (void)syncAll:(id)sender {
  ssMenuAction(1);
}
- (void)applicationWillTerminate:(NSNotification *)n {
  ssWillTerminate();
}
- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
  [self showWindow:nil];
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
// The page's header is the title bar: it asks to drag or zoom the window.
- (void)userContentController:(WKUserContentController *)uc didReceiveScriptMessage:(WKScriptMessage *)m {
  NSString *a = [m.body isKindOfClass:[NSString class]] ? m.body : @"";
  if ([a isEqualToString:@"drag"] && self.web.lastDown) {
    [self.window performWindowDragWithEvent:self.web.lastDown];
  } else if ([a isEqualToString:@"zoom"]) {
    NSString *act = [[NSUserDefaults standardUserDefaults] stringForKey:@"AppleActionOnDoubleClick"];
    if ([act isEqualToString:@"Minimize"]) [self.window performMiniaturize:nil];
    else if (![act isEqualToString:@"None"]) [self.window performZoom:nil];
  }
}
// Keep the window buttons centred in the page's header.
- (void)placeLights {
  NSWindow *w = self.window;
  NSButton *close = [w standardWindowButton:NSWindowCloseButton];
  NSButton *mini = [w standardWindowButton:NSWindowMiniaturizeButton];
  NSButton *zoom = [w standardWindowButton:NSWindowZoomButton];
  NSView *bar = close.superview.superview; // the title bar's container
  if (!close || !bar || (w.styleMask & NSWindowStyleMaskFullScreen)) return;
  CGFloat bh = NSHeight(close.frame);
  // The container grows down from the window's top; the buttons keep their
  // offset from its bottom, so its height sets how far down they sit.
  CGFloat top = floor((kHeader - bh) / 2);
  NSRect f = bar.frame;
  f.size.height = top + bh + NSMinY(close.frame);
  f.origin.y = NSHeight(w.frame) - f.size.height;
  bar.frame = f;
  CGFloat gap = NSMinX(mini.frame) - NSMinX(close.frame);
  NSArray *bs = @[ close, mini, zoom ];
  for (NSUInteger i = 0; i < bs.count; i++) {
    NSButton *b = bs[i];
    [b setFrameOrigin:NSMakePoint(kLightsX + gap * i, NSMinY(b.frame))];
  }
}
- (void)windowDidResize:(NSNotification *)n {
  [self placeLights];
}
// AppKit lays the title bar out again at these moments; put them back.
- (void)windowDidBecomeKey:(NSNotification *)n {
  [self placeLights];
}
- (void)windowDidResignKey:(NSNotification *)n {
  [self placeLights];
}
- (void)windowDidBecomeMain:(NSNotification *)n {
  [self placeLights];
}
- (void)windowDidEnterFullScreen:(NSNotification *)n {
  [self.web evaluateJavaScript:@"document.body.classList.add('fullscreen')" completionHandler:nil];
}
- (void)windowDidExitFullScreen:(NSNotification *)n {
  [self.web evaluateJavaScript:@"document.body.classList.remove('fullscreen')" completionHandler:nil];
  [self placeLights];
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

static void buildStatusItem(NSString *name) {
  NSStatusItem *it = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
  NSImage *img = [NSImage imageWithSystemSymbolName:@"arrow.triangle.2.circlepath" accessibilityDescription:name];
  img.template = YES;
  it.button.image = img;
  it.button.toolTip = name;
  NSMenu *m = [NSMenu new];
  [m addItem:item([@"Open " stringByAppendingString:name], @selector(showWindow:), @"", 0, gDelegate)];
  [m addItem:item(@"Sync All Playlists Now", @selector(syncAll:), @"", 0, gDelegate)];
  [m addItem:[NSMenuItem separatorItem]];
  [m addItem:item([@"Quit " stringByAppendingString:name], @selector(terminate:), @"q", NSEventModifierFlagCommand, nil)];
  it.menu = m;
  gDelegate.status = it;
}

void ssSetKeepRunning(int keep) {
  gKeepRunning = keep != 0;
}

void ssRun(const char *url, const char *title, int w, int h, int hidden) {
  @autoreleasepool {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    gDelegate = [SSDelegate new];
    NSApp.delegate = gDelegate;
    NSApp.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
    NSString *name = [NSString stringWithUTF8String:title];
    buildMenu(name);
    buildStatusItem(name);

    NSRect frame = NSMakeRect(0, 0, w, h);
    NSWindow *win = [[NSWindow alloc]
        initWithContentRect:frame
                  styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable |
                            NSWindowStyleMaskResizable | NSWindowStyleMaskFullSizeContentView
                    backing:NSBackingStoreBuffered
                      defer:NO];
    win.title = name;
    // No system title bar: the page's header takes its place (the window
    // buttons stay, placed in the header).
    win.titlebarAppearsTransparent = YES;
    win.titleVisibility = NSWindowTitleHidden;
    win.minSize = NSMakeSize(720, 520);
    win.backgroundColor = [NSColor colorWithSRGBRed:15 / 255.0 green:15 / 255.0 blue:16 / 255.0 alpha:1];
    win.releasedWhenClosed = NO;
    win.delegate = gDelegate;
    win.tabbingMode = NSWindowTabbingModeDisallowed;
    if (![win setFrameUsingName:@"SuperSync"]) [win center];
    win.frameAutosaveName = @"SuperSync";

    WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
    cfg.mediaTypesRequiringUserActionForPlayback = WKAudiovisualMediaTypeNone;
    [cfg.userContentController addScriptMessageHandler:gDelegate name:@"ss"];
    SSWebView *web = [[SSWebView alloc] initWithFrame:frame configuration:cfg];
    [web setValue:@NO forKey:@"drawsBackground"]; // the window's dark background shows while loading
    web.UIDelegate = gDelegate;
    web.navigationDelegate = gDelegate;
    web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    win.contentView = web;

    gDelegate.window = win;
    gDelegate.web = web;
    [gDelegate placeLights];
    gDelegate.home = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
    [web loadRequest:[NSURLRequest requestWithURL:gDelegate.home]];
    if (hidden) {
      // Opened at login: run in the menu bar until the window's wanted.
      [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    } else {
      [win makeKeyAndOrderFront:nil];
      [NSApp activateIgnoringOtherApps:YES];
    }
    dispatch_async(dispatch_get_main_queue(), ^{
      [gDelegate placeLights];
    });
    [NSApp run];
  }
}

void ssFocus(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    [gDelegate showWindow:nil];
  });
}

// ssEdit sends a standard edit command (paste:, cut:, copy:, selectAll:,
// undo:, redo:) to whatever has focus: the text field in the page.
void ssEdit(const char *sel) {
  NSString *s = [NSString stringWithUTF8String:sel];
  dispatch_async(dispatch_get_main_queue(), ^{
    [NSApp sendAction:NSSelectorFromString(s) to:nil from:nil];
  });
}

// ssActive: is the window on screen and SuperSync the app in front?
int ssActive(void) {
  __block int r = 0;
  void (^check)(void) = ^{
    r = [NSApp isActive] && gDelegate.window.isVisible && !gDelegate.window.isMiniaturized;
  };
  if ([NSThread isMainThread]) check(); else dispatch_sync(dispatch_get_main_queue(), check);
  return r;
}

// Clicking a notification brings the window back.
@interface SSNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end
@implementation SSNotifyDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)c didReceiveNotificationResponse:(UNNotificationResponse *)r
         withCompletionHandler:(void (^)(void))done {
  [gDelegate showWindow:nil];
  done();
}
@end
static SSNotifyDelegate *gNotify;

// AppleScript's notification: works without permission, for a copy of
// SuperSync macOS won't let post its own (not in an app bundle, or refused).
static void scriptNotify(NSString *t, NSString *b) {
  NSString *(^q)(NSString *) = ^(NSString *s) {
    return [[s stringByReplacingOccurrencesOfString:@"\\" withString:@"\\\\"] stringByReplacingOccurrencesOfString:@"\"" withString:@"\\\""];
  };
  NSTask *task = [NSTask new];
  task.launchPath = @"/usr/bin/osascript";
  task.arguments = @[@"-e", [NSString stringWithFormat:@"display notification \"%@\" with title \"%@\"", q(b), q(t)]];
  @try { [task launch]; } @catch (NSException *e) {}
}

void ssNotify(const char *title, const char *body) {
  NSString *t = [NSString stringWithUTF8String:title], *b = [NSString stringWithUTF8String:body];
  dispatch_async(dispatch_get_main_queue(), ^{
    if (NSBundle.mainBundle.bundleIdentifier == nil) { scriptNotify(t, b); return; }
    UNUserNotificationCenter *c = [UNUserNotificationCenter currentNotificationCenter];
    if (!gNotify) { gNotify = [SSNotifyDelegate new]; c.delegate = gNotify; }
    [c requestAuthorizationWithOptions:UNAuthorizationOptionAlert completionHandler:^(BOOL granted, NSError *err) {
      if (!granted) {
        if (err) dispatch_async(dispatch_get_main_queue(), ^{ scriptNotify(t, b); }); // not allowed to ask: fall back
        return; // the user said no: respect it
      }
      UNMutableNotificationContent *n = [UNMutableNotificationContent new];
      n.title = t;
      n.body = b;
      UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString] content:n trigger:nil];
      [c addNotificationRequest:req withCompletionHandler:nil];
    }];
  });
}

void ssClose(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    [NSApp terminate:nil];
  });
}
