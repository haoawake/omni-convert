// macOS 界面的 AppKit 部分：窗口、菜单、控件、文件列表、对话框。
// 排版和逻辑在 Go 里（ui_darwin.go），这里只按 Go 的吩咐创建、摆放控件，再把用户的操作转回 Go。
// 所有函数都只在主线程上调用（ui_post 除外）。

#import <Cocoa/Cocoa.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <stdlib.h>
#include <string.h>
#include "ui_darwin.h"
#include "_cgo_export.h"

static NSString *str(const char *s) { return s ? [NSString stringWithUTF8String:s] ?: @"" : @""; }
static char *cstr(NSString *s) { return strdup(s ? s.UTF8String : ""); }

static NSArray<NSString *> *lines(const char *s) {
	NSString *t = str(s);
	if (t.length == 0) return @[];
	return [t componentsSeparatedByString:@"\n"];
}

static NSFont *fontFor(int f) {
	switch (f) {
	case UI_FONT_BOLD: return [NSFont boldSystemFontOfSize:13];
	case UI_FONT_SMALL: return [NSFont systemFontOfSize:11];
	case UI_FONT_TITLE: return [NSFont systemFontOfSize:24 weight:NSFontWeightBold];
	case UI_FONT_HEADLINE: return [NSFont systemFontOfSize:15 weight:NSFontWeightSemibold];
	}
	return [NSFont systemFontOfSize:13];
}

static NSColor *toneColor(int t) {
	switch (t) {
	case UI_TONE_TEXT2: return NSColor.secondaryLabelColor;
	case UI_TONE_TEXT3: return NSColor.tertiaryLabelColor;
	case UI_TONE_ACCENT: return NSColor.controlAccentColor;
	case UI_TONE_OK: return NSColor.systemGreenColor;
	case UI_TONE_DANGER: return NSColor.systemRedColor;
	case UI_TONE_WARN: return NSColor.systemOrangeColor;
	}
	return NSColor.labelColor;
}

static NSImage *symbol(NSString *name, CGFloat size, NSFontWeight weight) {
	NSImage *img = [NSImage imageWithSystemSymbolName:name accessibilityDescription:nil];
	if (!img) return nil;
	NSImageSymbolConfiguration *cfg = [NSImageSymbolConfiguration configurationWithPointSize:size weight:weight];
	return [img imageWithSymbolConfiguration:cfg];
}

// ---------------------------------------------------------------- 自己画的几种视图

// 原点在左上角的视图（和 Go 那边的坐标一致）
@interface OCFlipped : NSView
@end
@implementation OCFlipped
- (BOOL)isFlipped { return YES; }
@end

// 窗口内容：接收拖进来的文件和文件夹
@interface OCRoot : OCFlipped
@property BOOL dragging;
@end

// 虚线框的拖放区域（拖着文件经过时变成蓝色）
@interface OCDropZone : NSView
@property BOOL hot;
@end
@implementation OCDropZone
- (BOOL)isFlipped { return YES; }
- (void)drawRect:(NSRect)dirty {
	NSRect r = NSInsetRect(self.bounds, 1.5, 1.5);
	NSBezierPath *p = [NSBezierPath bezierPathWithRoundedRect:r xRadius:12 yRadius:12];
	if (self.hot) {
		[[NSColor.controlAccentColor colorWithAlphaComponent:0.08] setFill];
		[p fill];
	}
	CGFloat dash[] = {6, 4};
	[p setLineDash:dash count:2 phase:0];
	p.lineWidth = 1.5;
	[(self.hot ? NSColor.controlAccentColor : NSColor.separatorColor) setStroke];
	[p stroke];
}
@end

// 文件列表：右键菜单、删除键、⌥↑ / ⌥↓ 调整顺序
@interface OCList : NSTableView
@end

// 「状态」一栏：文字，或者进度条加百分比
@interface OCStateCell : NSTableCellView
@property(strong) NSProgressIndicator *bar;
@property(strong) NSTextField *pct;
@end
@implementation OCStateCell
@end

// 左边分类的一行：图标、名字、右边的文件数
@interface OCNavCell : NSTableCellView
@property(strong) NSTextField *badge;
@end
@implementation OCNavCell
@end

// 文件列表一行的数据（从 Go 取一次，画这一行的几栏时共用）
@interface OCRowData : NSObject
@property(copy) NSString *name, *path, *size, *state, *result;
@property int nameTone, stateTone, resultTone, running;
@property double frac;
@end
@implementation OCRowData
@end

// ---------------------------------------------------------------- 全局状态

@interface OCApp : NSObject <NSApplicationDelegate, NSWindowDelegate, NSTableViewDataSource, NSTableViewDelegate,
                             NSTextFieldDelegate, NSMenuItemValidation, NSMenuDelegate>
@end

static OCApp *app;
static NSWindow *win;
static OCRoot *root;
static NSVisualEffectView *sideFX;
static NSTableView *navTable;
static NSTextField *verLabel;
static NSScrollView *listScroll;
static OCList *listTable;
static NSMutableDictionary<NSNumber *, NSView *> *views;
static NSMutableDictionary<NSNumber *, NSNumber *> *kinds;
static NSMutableDictionary<NSNumber *, NSString *> *itemsCache;
static NSMutableDictionary<NSNumber *, NSTimer *> *timers;
static NSMutableDictionary<NSNumber *, OCRowData *> *rowCache;
static NSArray<NSString *> *navTitles, *navSymbols, *navBadges;
static int nextID = 1;
static BOOL syncing; // 程序在改控件的状态，不要当成用户操作
static id activity;  // 转换期间不让电脑睡眠
static const CGFloat sideWidth = 200;

static NSView *viewFor(int ident) { return views[@(ident)]; }
static int kindOf(int ident) { return kinds[@(ident)].intValue; }
static int idOf(NSView *v) {
	for (NSNumber *k in views)
		if (views[k] == v) return k.intValue;
	return 0;
}

// ---------------------------------------------------------------- 文件列表

@implementation OCList
- (NSMenu *)menuForEvent:(NSEvent *)event {
	NSPoint p = [self convertPoint:event.locationInWindow fromView:nil];
	NSInteger row = [self rowAtPoint:p];
	if (row < 0) return nil;
	if (![self isRowSelected:row]) [self selectRowIndexes:[NSIndexSet indexSetWithIndex:row] byExtendingSelection:NO];
	char *spec = goListMenu((int)row);
	NSArray<NSString *> *items = lines(spec);
	free(spec);
	NSMenu *m = [[NSMenu alloc] initWithTitle:@""];
	m.autoenablesItems = NO;
	for (NSString *line in items) {
		if ([line isEqualToString:@"-"]) {
			[m addItem:NSMenuItem.separatorItem];
			continue;
		}
		NSArray<NSString *> *f = [line componentsSeparatedByString:@"\t"];
		if (f.count < 3) continue;
		NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:f[1] action:@selector(listCommand:) keyEquivalent:@""];
		it.tag = f[0].integerValue;
		it.target = app;
		it.enabled = [f[2] isEqualToString:@"1"];
		if (f.count > 3 && f[3].length > 0) {
			// 只是显示快捷键，按键本身由 keyDown 处理
			if ([f[3] isEqualToString:@"del"]) {
				it.keyEquivalent = @"\x08";
				it.keyEquivalentModifierMask = 0;
			} else if ([f[3] isEqualToString:@"up"]) {
				it.keyEquivalent = [NSString stringWithFormat:@"%C", (unichar)NSUpArrowFunctionKey];
				it.keyEquivalentModifierMask = NSEventModifierFlagOption;
			} else if ([f[3] isEqualToString:@"down"]) {
				it.keyEquivalent = [NSString stringWithFormat:@"%C", (unichar)NSDownArrowFunctionKey];
				it.keyEquivalentModifierMask = NSEventModifierFlagOption;
			}
		}
		[m addItem:it];
	}
	return m;
}
- (void)keyDown:(NSEvent *)e {
	NSString *c = e.charactersIgnoringModifiers;
	unichar k = c.length ? [c characterAtIndex:0] : 0;
	BOOL opt = (e.modifierFlags & NSEventModifierFlagOption) != 0;
	if (k == NSDeleteCharacter || k == NSBackspaceCharacter || k == NSDeleteFunctionKey) {
		goListKey(1);
		return;
	}
	if (opt && k == NSUpArrowFunctionKey) {
		goListKey(2);
		return;
	}
	if (opt && k == NSDownArrowFunctionKey) {
		goListKey(3);
		return;
	}
	if (k == '\r' || k == 3) {
		goListDouble((int)self.selectedRow);
		return;
	}
	[super keyDown:e];
}
- (void)delete:(id)sender { goListKey(1); }
@end

// ---------------------------------------------------------------- 拖放

static NSArray<NSString *> *filesFrom(NSPasteboard *pb) {
	NSArray<NSURL *> *urls = [pb readObjectsForClasses:@[ NSURL.class ] options:@{NSPasteboardURLReadingFileURLsOnlyKey : @YES}];
	NSMutableArray *out = [NSMutableArray array];
	for (NSURL *u in urls)
		if (u.isFileURL && u.path) [out addObject:u.path];
	return out;
}

static OCDropZone *dropZone(void) {
	for (NSView *v in views.allValues)
		if ([v isKindOfClass:OCDropZone.class] && !v.hidden) return (OCDropZone *)v;
	return nil;
}

@implementation OCRoot
- (NSDragOperation)draggingEntered:(id<NSDraggingInfo>)info {
	if (filesFrom(info.draggingPasteboard).count == 0) return NSDragOperationNone;
	dropZone().hot = YES;
	[dropZone() setNeedsDisplay:YES];
	return NSDragOperationCopy;
}
- (NSDragOperation)draggingUpdated:(id<NSDraggingInfo>)info {
	return filesFrom(info.draggingPasteboard).count ? NSDragOperationCopy : NSDragOperationNone;
}
- (void)draggingExited:(id<NSDraggingInfo>)info {
	dropZone().hot = NO;
	[dropZone() setNeedsDisplay:YES];
}
- (BOOL)performDragOperation:(id<NSDraggingInfo>)info {
	dropZone().hot = NO;
	[dropZone() setNeedsDisplay:YES];
	NSArray *files = filesFrom(info.draggingPasteboard);
	if (files.count == 0) return NO;
	NSString *joined = [files componentsJoinedByString:@"\n"];
	// 拖放结束后再处理，免得访达那边一直等着
	dispatch_async(dispatch_get_main_queue(), ^{
		goDrop((char *)joined.UTF8String);
		[NSApp activateIgnoringOtherApps:YES];
	});
	return YES;
}
@end

// ---------------------------------------------------------------- 程序

@implementation OCApp

- (void)applicationDidFinishLaunching:(NSNotification *)n {
	[NSApp activateIgnoringOtherApps:YES];
	goLaunched();
}
- (void)application:(NSApplication *)sender openURLs:(NSArray<NSURL *> *)urls {
	NSMutableArray *paths = [NSMutableArray array];
	for (NSURL *u in urls)
		if (u.isFileURL && u.path) [paths addObject:u.path];
	if (paths.count) goOpenFiles((char *)[paths componentsJoinedByString:@"\n"].UTF8String);
}
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)s { return YES; }
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)s {
	return goShouldQuit() ? NSTerminateNow : NSTerminateCancel;
}
- (void)applicationWillTerminate:(NSNotification *)n { goWillQuit(); }
- (void)applicationDidBecomeActive:(NSNotification *)n { goActivated(); }
- (BOOL)applicationSupportsSecureRestorableState:(NSApplication *)a { return YES; }

- (BOOL)windowShouldClose:(NSWindow *)w { return goShouldQuit() != 0; }
- (void)windowDidResize:(NSNotification *)n { goResize(); }

// 菜单
- (void)menuCommand:(NSMenuItem *)it { goMenu((int)it.tag); }
- (void)listCommand:(NSMenuItem *)it { goListCommand((int)it.tag); }
- (BOOL)validateMenuItem:(NSMenuItem *)it {
	if (it.action == @selector(menuCommand:)) return goMenuEnabled((int)it.tag) != 0;
	if (it.action == @selector(paste:)) return YES;
	return YES;
}
// 没有输入框在用时，⌘V 粘贴在访达里拷贝的文件
- (void)paste:(id)sender { goPaste(); }

// 控件
- (void)controlAction:(id)sender {
	if (syncing) return;
	int ident = idOf(sender);
	int value = 0;
	if ([sender isKindOfClass:NSPopUpButton.class]) value = (int)((NSPopUpButton *)sender).indexOfSelectedItem;
	else if ([sender isKindOfClass:NSSegmentedControl.class]) value = (int)((NSSegmentedControl *)sender).selectedSegment;
	else if ([sender isKindOfClass:NSButton.class]) value = ((NSButton *)sender).state == NSControlStateValueOn;
	goAction(ident, value);
}
- (void)controlTextDidChange:(NSNotification *)n {
	if (syncing) return;
	NSTextField *f = n.object;
	if (kindOf(idOf(f)) == UI_NUMBER) {
		// 只能输入数字（Windows 版的 ES_NUMBER）
		NSString *s = f.stringValue;
		NSMutableString *d = [NSMutableString string];
		for (NSUInteger i = 0; i < s.length; i++) {
			unichar c = [s characterAtIndex:i];
			if ((c >= '0' && c <= '9') || c == '.') [d appendFormat:@"%C", c];
			else if (c >= 0xFF10 && c <= 0xFF19) [d appendFormat:@"%C", (unichar)(c - 0xFF10 + '0')];
		}
		if (![d isEqualToString:s]) f.stringValue = d;
	}
	goText(idOf(f), (char *)f.stringValue.UTF8String);
}
- (BOOL)control:(NSControl *)c textView:(NSTextView *)tv doCommandBySelector:(SEL)sel {
	if (sel == @selector(insertNewline:)) {
		[win makeFirstResponder:nil]; // 回车：结束输入
		return YES;
	}
	return NO;
}

// 列表和左边的分类
- (NSInteger)numberOfRowsInTableView:(NSTableView *)t {
	if (t == navTable) return navTitles.count;
	return goListCount();
}

static OCRowData *rowData(NSInteger row) {
	OCRowData *d = rowCache[@(row)];
	if (d) return d;
	UIRow r;
	memset(&r, 0, sizeof r);
	goListRow((int)row, &r);
	d = [OCRowData new];
	d.name = str(r.name);
	d.path = str(r.path);
	d.size = str(r.size);
	d.state = str(r.state);
	d.result = str(r.result);
	free(r.name);
	free(r.path);
	free(r.size);
	free(r.state);
	free(r.result);
	d.nameTone = r.nameTone;
	d.stateTone = r.stateTone;
	d.resultTone = r.resultTone;
	d.frac = r.frac;
	d.running = r.running;
	rowCache[@(row)] = d;
	return d;
}

static NSTextField *cellLabel(void) {
	NSTextField *t = [NSTextField labelWithString:@""];
	t.lineBreakMode = NSLineBreakByTruncatingTail;
	t.translatesAutoresizingMaskIntoConstraints = NO;
	t.font = [NSFont systemFontOfSize:13];
	[t setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
	return t;
}

static NSImage *iconFor(NSString *path) {
	static NSMutableDictionary<NSString *, NSImage *> *cache;
	if (!cache) cache = [NSMutableDictionary dictionary];
	NSString *ext = path.pathExtension.lowercaseString;
	NSImage *img = cache[ext];
	if (!img) {
		UTType *t = [UTType typeWithFilenameExtension:ext];
		img = t ? [NSWorkspace.sharedWorkspace iconForContentType:t] : [NSWorkspace.sharedWorkspace iconForFile:path];
		cache[ext] = img;
	}
	return img;
}

static void configureCell(NSTableCellView *cell, NSString *col, OCRowData *d) {
	if ([col isEqualToString:@"name"]) {
		cell.textField.stringValue = d.name;
		cell.textField.textColor = toneColor(d.nameTone);
		cell.imageView.image = iconFor(d.path);
		cell.toolTip = d.path;
	} else if ([col isEqualToString:@"size"]) {
		cell.textField.stringValue = d.size;
		cell.textField.textColor = toneColor(d.nameTone);
	} else if ([col isEqualToString:@"result"]) {
		cell.textField.stringValue = d.result;
		cell.textField.textColor = toneColor(d.resultTone);
		cell.toolTip = d.result.length > 20 ? d.result : nil;
	} else if ([col isEqualToString:@"state"]) {
		OCStateCell *s = (OCStateCell *)cell;
		BOOL run = d.running != 0;
		s.textField.hidden = run;
		s.bar.hidden = !run;
		s.pct.hidden = !run;
		if (run) {
			if (d.frac < 0) {
				if (!s.bar.indeterminate) s.bar.indeterminate = YES;
				[s.bar startAnimation:nil];
				s.pct.stringValue = @"…";
			} else {
				if (s.bar.indeterminate) {
					[s.bar stopAnimation:nil];
					s.bar.indeterminate = NO;
				}
				s.bar.doubleValue = MIN(MAX(d.frac, 0), 1) * 100;
				s.pct.stringValue = [NSString stringWithFormat:@"%d%%", (int)(d.frac * 100)];
			}
		} else {
			[s.bar stopAnimation:nil];
			s.textField.stringValue = d.state;
			s.textField.textColor = toneColor(d.stateTone);
		}
	}
}

- (NSView *)tableView:(NSTableView *)t viewForTableColumn:(NSTableColumn *)c row:(NSInteger)row {
	if (t == navTable) {
		OCNavCell *cell = [t makeViewWithIdentifier:@"nav" owner:self];
		if (!cell) {
			cell = [[OCNavCell alloc] initWithFrame:NSMakeRect(0, 0, 180, 32)];
			cell.identifier = @"nav";
			NSImageView *iv = [NSImageView new];
			iv.translatesAutoresizingMaskIntoConstraints = NO;
			NSTextField *tf = cellLabel();
			NSTextField *badge = [NSTextField labelWithString:@""];
			badge.translatesAutoresizingMaskIntoConstraints = NO;
			badge.font = [NSFont monospacedDigitSystemFontOfSize:12 weight:NSFontWeightRegular];
			badge.textColor = NSColor.secondaryLabelColor;
			badge.alignment = NSTextAlignmentRight;
			[cell addSubview:iv];
			[cell addSubview:tf];
			[cell addSubview:badge];
			cell.imageView = iv;
			cell.textField = tf;
			cell.badge = badge;
			[NSLayoutConstraint activateConstraints:@[
				[iv.leadingAnchor constraintEqualToAnchor:cell.leadingAnchor constant:4],
				[iv.centerYAnchor constraintEqualToAnchor:cell.centerYAnchor],
				[iv.widthAnchor constraintEqualToConstant:22],
				[tf.leadingAnchor constraintEqualToAnchor:iv.trailingAnchor constant:8],
				[tf.centerYAnchor constraintEqualToAnchor:cell.centerYAnchor],
				[badge.leadingAnchor constraintGreaterThanOrEqualToAnchor:tf.trailingAnchor constant:4],
				[badge.trailingAnchor constraintEqualToAnchor:cell.trailingAnchor constant:-6],
				[badge.centerYAnchor constraintEqualToAnchor:cell.centerYAnchor],
			]];
		}
		cell.textField.stringValue = row < navTitles.count ? navTitles[row] : @"";
		cell.textField.font = [NSFont systemFontOfSize:14];
		cell.imageView.image = row < navSymbols.count ? symbol(navSymbols[row], 15, NSFontWeightRegular) : nil;
		cell.badge.stringValue = row < navBadges.count ? navBadges[row] : @"";
		return cell;
	}

	NSString *col = c.identifier;
	OCRowData *d = rowData(row);
	NSTableCellView *cell = [t makeViewWithIdentifier:col owner:self];
	if (!cell) {
		if ([col isEqualToString:@"state"]) {
			OCStateCell *s = [[OCStateCell alloc] initWithFrame:NSZeroRect];
			NSTextField *tf = cellLabel();
			NSProgressIndicator *bar = [NSProgressIndicator new];
			bar.style = NSProgressIndicatorStyleBar;
			bar.controlSize = NSControlSizeSmall;
			bar.indeterminate = NO;
			bar.minValue = 0;
			bar.maxValue = 100;
			bar.translatesAutoresizingMaskIntoConstraints = NO;
			NSTextField *pct = cellLabel();
			pct.font = [NSFont monospacedDigitSystemFontOfSize:11 weight:NSFontWeightRegular];
			pct.textColor = NSColor.secondaryLabelColor;
			[s addSubview:tf];
			[s addSubview:bar];
			[s addSubview:pct];
			s.textField = tf;
			s.bar = bar;
			s.pct = pct;
			[NSLayoutConstraint activateConstraints:@[
				[tf.leadingAnchor constraintEqualToAnchor:s.leadingAnchor constant:2],
				[tf.trailingAnchor constraintEqualToAnchor:s.trailingAnchor constant:-2],
				[tf.centerYAnchor constraintEqualToAnchor:s.centerYAnchor],
				[bar.leadingAnchor constraintEqualToAnchor:s.leadingAnchor constant:2],
				[bar.centerYAnchor constraintEqualToAnchor:s.centerYAnchor],
				[pct.leadingAnchor constraintEqualToAnchor:bar.trailingAnchor constant:6],
				[pct.trailingAnchor constraintEqualToAnchor:s.trailingAnchor constant:-2],
				[pct.widthAnchor constraintEqualToConstant:34],
				[pct.centerYAnchor constraintEqualToAnchor:s.centerYAnchor],
			]];
			cell = s;
		} else {
			cell = [[NSTableCellView alloc] initWithFrame:NSZeroRect];
			NSTextField *tf = cellLabel();
			[cell addSubview:tf];
			cell.textField = tf;
			if ([col isEqualToString:@"name"]) {
				NSImageView *iv = [NSImageView new];
				iv.translatesAutoresizingMaskIntoConstraints = NO;
				[cell addSubview:iv];
				cell.imageView = iv;
				[NSLayoutConstraint activateConstraints:@[
					[iv.leadingAnchor constraintEqualToAnchor:cell.leadingAnchor constant:2],
					[iv.centerYAnchor constraintEqualToAnchor:cell.centerYAnchor],
					[iv.widthAnchor constraintEqualToConstant:16],
					[iv.heightAnchor constraintEqualToConstant:16],
					[tf.leadingAnchor constraintEqualToAnchor:iv.trailingAnchor constant:6],
				]];
			} else {
				[tf.leadingAnchor constraintEqualToAnchor:cell.leadingAnchor constant:2].active = YES;
			}
			if ([col isEqualToString:@"size"]) {
				tf.alignment = NSTextAlignmentRight;
				tf.font = [NSFont monospacedDigitSystemFontOfSize:13 weight:NSFontWeightRegular];
			}
			[tf.trailingAnchor constraintEqualToAnchor:cell.trailingAnchor constant:-2].active = YES;
			[tf.centerYAnchor constraintEqualToAnchor:cell.centerYAnchor].active = YES;
		}
		cell.identifier = col;
	}
	configureCell(cell, col, d);
	return cell;
}

- (void)tableViewSelectionDidChange:(NSNotification *)n {
	if (n.object == navTable && !syncing && navTable.selectedRow >= 0) goNav((int)navTable.selectedRow);
}

- (void)listDoubleClick:(id)sender {
	if (listTable.clickedRow >= 0) goListDouble((int)listTable.clickedRow);
}

- (void)timerFired:(NSTimer *)t { goTimer([t.userInfo intValue]); }

@end

// ---------------------------------------------------------------- 菜单

// 菜单命令的编号（和 ui_darwin.go 里的 menuXxx 一致）
enum { M_ABOUT = 1, M_ADD, M_START, M_STOP, M_REMOVE, M_CLEAR, M_REVEAL, M_HOME, M_LIBREOFFICE, M_PAGE = 100 };

static NSMenuItem *cmd(NSMenu *m, NSString *title, int tag, NSString *key, NSEventModifierFlags mods) {
	NSMenuItem *it = [m addItemWithTitle:title action:@selector(menuCommand:) keyEquivalent:key];
	it.tag = tag;
	it.target = app;
	it.keyEquivalentModifierMask = mods;
	return it;
}

static NSMenuItem *sys(NSMenu *m, NSString *title, SEL action, NSString *key, NSEventModifierFlags mods) {
	NSMenuItem *it = [m addItemWithTitle:title action:action keyEquivalent:key];
	it.keyEquivalentModifierMask = mods;
	return it;
}

static NSMenu *submenu(NSMenu *bar, NSString *title) {
	NSMenuItem *top = [bar addItemWithTitle:title action:nil keyEquivalent:@""];
	NSMenu *m = [[NSMenu alloc] initWithTitle:title];
	top.submenu = m;
	return m;
}

static void buildMenus(NSString *name) {
	NSEventModifierFlags C = NSEventModifierFlagCommand, O = NSEventModifierFlagOption, S = NSEventModifierFlagShift;
	NSMenu *bar = [NSMenu new];

	NSMenu *m = submenu(bar, name);
	cmd(m, [NSString stringWithFormat:@"关于%@", name], M_ABOUT, @"", 0);
	[m addItem:NSMenuItem.separatorItem];
	NSMenuItem *services = [m addItemWithTitle:@"服务" action:nil keyEquivalent:@""];
	services.submenu = [[NSMenu alloc] initWithTitle:@"服务"];
	NSApp.servicesMenu = services.submenu;
	[m addItem:NSMenuItem.separatorItem];
	sys(m, [NSString stringWithFormat:@"隐藏%@", name], @selector(hide:), @"h", C);
	sys(m, @"隐藏其他", @selector(hideOtherApplications:), @"h", C | O);
	sys(m, @"全部显示", @selector(unhideAllApplications:), @"", 0);
	[m addItem:NSMenuItem.separatorItem];
	sys(m, [NSString stringWithFormat:@"退出%@", name], @selector(terminate:), @"q", C);

	m = submenu(bar, @"文件");
	cmd(m, @"添加文件…", M_ADD, @"o", C);
	[m addItem:NSMenuItem.separatorItem];
	cmd(m, @"开始转换", M_START, @"\r", C);
	cmd(m, @"停止", M_STOP, @".", C);
	[m addItem:NSMenuItem.separatorItem];
	cmd(m, @"在访达中显示转换结果", M_REVEAL, @"r", C);
	// 不设 ⌘⌫：输入框里 ⌘⌫ 是删到行首（列表里直接按 ⌫ 就能移除）
	cmd(m, @"从列表中移除", M_REMOVE, @"", 0);
	cmd(m, @"清空列表", M_CLEAR, @"", 0);
	[m addItem:NSMenuItem.separatorItem];
	sys(m, @"关闭窗口", @selector(performClose:), @"w", C);

	m = submenu(bar, @"编辑");
	sys(m, @"撤销", @selector(undo:), @"z", C);
	sys(m, @"重做", @selector(redo:), @"z", C | S);
	[m addItem:NSMenuItem.separatorItem];
	sys(m, @"剪切", @selector(cut:), @"x", C);
	sys(m, @"拷贝", @selector(copy:), @"c", C);
	sys(m, @"粘贴", @selector(paste:), @"v", C);
	sys(m, @"删除", @selector(delete:), @"", 0);
	sys(m, @"全选", @selector(selectAll:), @"a", C);

	m = submenu(bar, @"显示");
	NSArray *pages = @[ @"图片", @"视频", @"音频", @"文档", @"PDF" ];
	for (NSUInteger i = 0; i < pages.count; i++)
		cmd(m, pages[i], M_PAGE + (int)i, [NSString stringWithFormat:@"%lu", (unsigned long)i + 1], C);

	m = submenu(bar, @"窗口");
	sys(m, @"最小化", @selector(performMiniaturize:), @"m", C);
	sys(m, @"缩放", @selector(performZoom:), @"", 0);
	[m addItem:NSMenuItem.separatorItem];
	sys(m, @"前置全部窗口", @selector(arrangeInFront:), @"", 0);
	NSApp.windowsMenu = m;

	m = submenu(bar, @"帮助");
	cmd(m, [NSString stringWithFormat:@"%@主页", name], M_HOME, @"", 0);
	cmd(m, @"下载 LibreOffice（转换 Word、Excel、PPT 用）", M_LIBREOFFICE, @"", 0);
	NSApp.helpMenu = m;

	NSApp.mainMenu = bar;
}

// ---------------------------------------------------------------- 程序和窗口

void ui_init(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	app = [OCApp new];
	NSApp.delegate = app;
	views = [NSMutableDictionary dictionary];
	kinds = [NSMutableDictionary dictionary];
	itemsCache = [NSMutableDictionary dictionary];
	timers = [NSMutableDictionary dictionary];
	rowCache = [NSMutableDictionary dictionary];
}

void ui_window(const char *title, double w, double h, double minW, double minH) {
	NSString *name = str(title);
	buildMenus(name);
	NSRect vis = NSScreen.mainScreen.visibleFrame;
	w = MIN(w, vis.size.width * 0.92);
	h = MIN(h, vis.size.height * 0.92);
	NSRect r = NSMakeRect(vis.origin.x + (vis.size.width - w) / 2, vis.origin.y + (vis.size.height - h) / 2, w, h);
	win = [[NSWindow alloc] initWithContentRect:r
	                                  styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable |
	                                            NSWindowStyleMaskResizable | NSWindowStyleMaskFullSizeContentView
	                                    backing:NSBackingStoreBuffered
	                                      defer:NO];
	win.title = name;
	win.titleVisibility = NSWindowTitleHidden;
	win.titlebarAppearsTransparent = YES;
	win.contentMinSize = NSMakeSize(minW, minH);
	win.delegate = app;
	win.releasedWhenClosed = NO;
	win.tabbingMode = NSWindowTabbingModeDisallowed;
	[win setFrameAutosaveName:@"main"];

	root = [[OCRoot alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
	root.wantsLayer = YES;
	[root registerForDraggedTypes:@[ NSPasteboardTypeFileURL ]];
	win.contentView = root;

	// 左边的分类：系统侧边栏的半透明材质，一直铺到窗口顶上（红黄绿三个按钮在它上面）
	sideFX = [[NSVisualEffectView alloc] initWithFrame:NSMakeRect(0, 0, sideWidth, h)];
	sideFX.material = NSVisualEffectMaterialSidebar;
	sideFX.blendingMode = NSVisualEffectBlendingModeBehindWindow;
	sideFX.state = NSVisualEffectStateFollowsWindowActiveState;
	sideFX.autoresizingMask = NSViewHeightSizable;
	[root addSubview:sideFX];
	NSBox *line = [[NSBox alloc] initWithFrame:NSMakeRect(sideWidth - 1, 0, 1, h)];
	line.boxType = NSBoxSeparator;
	line.autoresizingMask = NSViewHeightSizable | NSViewMinXMargin;
	[sideFX addSubview:line];

	CGFloat top = 52;
	NSScrollView *ns = [[NSScrollView alloc] initWithFrame:NSMakeRect(0, 0, sideWidth, h - top - 40)];
	ns.drawsBackground = NO;
	ns.hasVerticalScroller = NO;
	ns.autoresizingMask = NSViewHeightSizable;
	navTable = [[NSTableView alloc] initWithFrame:ns.bounds];
	navTable.style = NSTableViewStyleSourceList;
	navTable.headerView = nil;
	navTable.rowHeight = 34;
	navTable.backgroundColor = NSColor.clearColor;
	navTable.focusRingType = NSFocusRingTypeNone;
	NSTableColumn *nc = [[NSTableColumn alloc] initWithIdentifier:@"nav"];
	nc.resizingMask = NSTableColumnAutoresizingMask;
	[navTable addTableColumn:nc];
	navTable.columnAutoresizingStyle = NSTableViewUniformColumnAutoresizingStyle;
	navTable.dataSource = app;
	navTable.delegate = app;
	ns.documentView = navTable;
	[sideFX addSubview:ns];
	// 侧边栏是不翻转的坐标：从下往上数
	ns.frame = NSMakeRect(0, 40, sideWidth, h - top - 40);

	verLabel = [NSTextField labelWithString:@""];
	verLabel.font = [NSFont systemFontOfSize:11];
	verLabel.textColor = NSColor.tertiaryLabelColor;
	verLabel.frame = NSMakeRect(20, 14, sideWidth - 30, 16);
	[sideFX addSubview:verLabel];
}

void ui_run(void) {
	[win makeKeyAndOrderFront:nil];
	[NSApp run];
}

void ui_quit(void) { [NSApp terminate:nil]; }

void ui_set_title(const char *title) {
	// 标题栏是透明的，标题不显示；改的是「窗口」菜单和调度中心里看到的名字
	win.title = str(title);
}

void ui_content_size(double *w, double *h) {
	NSSize s = root.bounds.size;
	*w = s.width;
	*h = s.height;
}

double ui_top_inset(void) {
	NSRect full = win.frame;
	NSRect content = [win contentRectForFrameRect:full];
	double t = NSHeight(full) - NSHeight(win.contentLayoutRect);
	(void)content;
	return t > 0 && t < 80 ? t : 28;
}

// ---------------------------------------------------------------- 控件

static void setTarget(NSControl *c) {
	c.target = app;
	c.action = @selector(controlAction:);
}

static NSTextField *newLabel(BOOL wrap) {
	NSTextField *t = wrap ? [NSTextField wrappingLabelWithString:@""] : [NSTextField labelWithString:@""];
	t.font = [NSFont systemFontOfSize:13];
	t.textColor = NSColor.labelColor;
	if (!wrap) t.lineBreakMode = NSLineBreakByTruncatingTail;
	t.selectable = NO;
	return t;
}

static NSBox *newBox(NSColor *fill, NSColor *border, CGFloat radius) {
	NSBox *b = [NSBox new];
	b.boxType = NSBoxCustom;
	b.titlePosition = NSNoTitle;
	b.fillColor = fill;
	b.borderColor = border;
	b.borderWidth = border ? 1 : 0;
	b.cornerRadius = radius;
	b.contentViewMargins = NSZeroSize;
	return b;
}

static void buildList(void) {
	listScroll = [NSScrollView new];
	listScroll.hasVerticalScroller = YES;
	listScroll.hasHorizontalScroller = NO;
	listScroll.autohidesScrollers = YES;
	listScroll.borderType = NSNoBorder;
	listScroll.drawsBackground = NO;
	listTable = [[OCList alloc] initWithFrame:NSZeroRect];
	listTable.style = NSTableViewStyleFullWidth;
	listTable.usesAlternatingRowBackgroundColors = YES;
	listTable.allowsMultipleSelection = YES;
	listTable.rowHeight = 26;
	listTable.intercellSpacing = NSMakeSize(8, 0);
	listTable.columnAutoresizingStyle = NSTableViewLastColumnOnlyAutoresizingStyle;
	NSArray *cols = @[ @[ @"name", @"文件", @220 ], @[ @"size", @"大小", @72 ], @[ @"state", @"状态", @120 ], @[ @"result", @"结果", @240 ] ];
	for (NSArray *c in cols) {
		NSTableColumn *tc = [[NSTableColumn alloc] initWithIdentifier:c[0]];
		tc.title = c[1];
		tc.width = [c[2] doubleValue];
		tc.minWidth = 50;
		if ([c[0] isEqualToString:@"size"]) tc.headerCell.alignment = NSTextAlignmentRight;
		[listTable addTableColumn:tc];
	}
	listTable.dataSource = app;
	listTable.delegate = app;
	listTable.target = app;
	listTable.doubleAction = @selector(listDoubleClick:);
	listScroll.documentView = listTable;
}

// fitColumns 按列表宽度分配四栏（和 Windows 版一样：大小、状态固定，剩下的文件名和结果对半分）
static void fitColumns(CGFloat width) {
	NSArray<NSTableColumn *> *c = listTable.tableColumns;
	if (c.count < 4) return;
	CGFloat size = 72, state = 128;
	CGFloat rest = MAX(width - size - state - 8 * 4 - 16, 240);
	CGFloat name = rest * 0.46;
	c[0].width = name;
	c[1].width = size;
	c[2].width = state;
	c[3].width = rest - name;
}

int ui_new(int kind, int parent) {
	NSView *v = nil;
	switch (kind) {
	case UI_LABEL: v = newLabel(NO); break;
	case UI_WRAP: v = newLabel(YES); break;
	case UI_BUTTON:
	case UI_PRIMARY:
	case UI_DANGER:
	case UI_CHIP: {
		NSButton *b = [NSButton buttonWithTitle:@"" target:app action:@selector(controlAction:)];
		b.bezelStyle = NSBezelStyleRounded;
		if (kind == UI_PRIMARY) b.bezelColor = NSColor.controlAccentColor;
		if (kind == UI_DANGER) b.bezelColor = NSColor.systemRedColor;
		if (kind == UI_PRIMARY || kind == UI_DANGER) b.controlSize = NSControlSizeLarge;
		v = b;
		break;
	}
	case UI_LINK: {
		NSButton *b = [NSButton buttonWithTitle:@"" target:app action:@selector(controlAction:)];
		b.bordered = NO;
		b.contentTintColor = NSColor.linkColor;
		v = b;
		break;
	}
	case UI_POPUP:
	case UI_PULLDOWN: {
		NSPopUpButton *p = [[NSPopUpButton alloc] initWithFrame:NSZeroRect pullsDown:kind == UI_PULLDOWN];
		setTarget(p);
		p.autoenablesItems = NO;
		v = p;
		break;
	}
	case UI_SEGMENT: {
		NSSegmentedControl *s = [NSSegmentedControl new];
		s.trackingMode = NSSegmentSwitchTrackingSelectOne;
		s.segmentStyle = NSSegmentStyleRounded;
		setTarget(s);
		v = s;
		break;
	}
	case UI_TEXT:
	case UI_NUMBER:
	case UI_SECURE: {
		NSTextField *t = kind == UI_SECURE ? [NSSecureTextField textFieldWithString:@""] : [NSTextField textFieldWithString:@""];
		t.delegate = app;
		t.font = [NSFont systemFontOfSize:13];
		t.bezeled = YES;
		t.bezelStyle = NSTextFieldRoundedBezel;
		t.usesSingleLineMode = YES;
		t.cell.scrollable = YES;
		t.cell.wraps = NO;
		v = t;
		break;
	}
	case UI_CHECK: {
		NSButton *b = [NSButton checkboxWithTitle:@"" target:app action:@selector(controlAction:)];
		v = b;
		break;
	}
	case UI_IMAGE: {
		NSImageView *iv = [NSImageView new];
		iv.imageScaling = NSImageScaleProportionallyUpOrDown;
		v = iv;
		break;
	}
	case UI_CARD: v = newBox(NSColor.controlBackgroundColor, NSColor.separatorColor, 10); break;
	case UI_BANNER: v = newBox([NSColor.systemOrangeColor colorWithAlphaComponent:0.12], [NSColor.systemOrangeColor colorWithAlphaComponent:0.35], 8); break;
	case UI_TOAST: v = newBox([NSColor.labelColor colorWithAlphaComponent:0.06], NSColor.separatorColor, 8); break;
	case UI_SCROLL: {
		NSScrollView *s = [NSScrollView new];
		s.hasVerticalScroller = YES;
		s.autohidesScrollers = YES;
		s.drawsBackground = NO;
		s.borderType = NSNoBorder;
		OCFlipped *doc = [[OCFlipped alloc] initWithFrame:NSMakeRect(0, 0, 100, 100)];
		s.documentView = doc;
		v = s;
		break;
	}
	case UI_DROPZONE: v = [OCDropZone new]; break;
	case UI_LIST:
		if (!listScroll) buildList();
		v = listScroll;
		break;
	case UI_LINE: {
		NSBox *b = [NSBox new];
		b.boxType = NSBoxSeparator;
		v = b;
		break;
	}
	case UI_SPINNER: {
		NSProgressIndicator *p = [NSProgressIndicator new];
		p.style = NSProgressIndicatorStyleSpinning;
		p.controlSize = NSControlSizeSmall;
		[p startAnimation:nil];
		v = p;
		break;
	}
	default: return 0;
	}
	int ident = nextID++;
	views[@(ident)] = v;
	kinds[@(ident)] = @(kind);
	NSView *p = parent ? viewFor(parent) : root;
	if ([p isKindOfClass:NSScrollView.class]) p = ((NSScrollView *)p).documentView;
	[p addSubview:v];
	return ident;
}

void ui_remove(int ident) {
	NSView *v = viewFor(ident);
	if (!v) return;
	if (v == listScroll) {
		v.hidden = YES;
		return;
	}
	if (win.firstResponder == v || ([win.firstResponder isKindOfClass:NSText.class] && ((NSText *)win.firstResponder).delegate == (id)v))
		[win makeFirstResponder:nil];
	[v removeFromSuperview];
	[views removeObjectForKey:@(ident)];
	[kinds removeObjectForKey:@(ident)];
	[itemsCache removeObjectForKey:@(ident)];
}

void ui_frame(int ident, double x, double y, double w, double h) {
	NSView *v = viewFor(ident);
	if (!v) return;
	// Go 给的是控件看得见的范围（对齐矩形），按钮、下拉框四周还有一圈阴影的留白，换算成 frame
	NSRect r = NSMakeRect(x, y, w, h);
	NSView *sup = v.superview;
	if (sup && !sup.isFlipped) r.origin.y = sup.bounds.size.height - y - h;
	if ([v isKindOfClass:NSControl.class] && ![v isKindOfClass:NSTextField.class]) r = [v frameForAlignmentRect:r];
	r = NSIntegralRectWithOptions(r, NSAlignAllEdgesNearest);
	if (!NSEqualRects(v.frame, r)) v.frame = r;
	if (v == listScroll) fitColumns(w);
}

static BOOL editing(NSView *v) {
	id fr = win.firstResponder;
	return [fr isKindOfClass:NSText.class] && ((NSText *)fr).delegate == (id)v;
}

void ui_text(int ident, const char *s) {
	NSView *v = viewFor(ident);
	NSString *t = str(s);
	syncing = YES;
	if ([v isKindOfClass:NSTextField.class]) {
		NSTextField *f = (NSTextField *)v;
		if (![f.stringValue isEqualToString:t] && !(f.editable && editing(f))) f.stringValue = t;
	} else if ([v isKindOfClass:NSButton.class]) {
		NSButton *b = (NSButton *)v;
		if (![b.title isEqualToString:t]) b.title = t;
	}
	syncing = NO;
}

void ui_placeholder(int ident, const char *s) {
	NSView *v = viewFor(ident);
	if ([v isKindOfClass:NSTextField.class]) ((NSTextField *)v).placeholderString = str(s);
}

void ui_font(int ident, int f) {
	NSView *v = viewFor(ident);
	if ([v isKindOfClass:NSControl.class]) ((NSControl *)v).font = fontFor(f);
}

void ui_tone(int ident, int t) {
	NSView *v = viewFor(ident);
	if ([v isKindOfClass:NSTextField.class]) ((NSTextField *)v).textColor = toneColor(t);
	else if ([v isKindOfClass:NSImageView.class]) ((NSImageView *)v).contentTintColor = toneColor(t);
	else if ([v isKindOfClass:NSButton.class]) ((NSButton *)v).contentTintColor = toneColor(t);
}

void ui_align(int ident, int align) {
	NSView *v = viewFor(ident);
	NSTextAlignment al = align == 1 ? NSTextAlignmentCenter : (align == 2 ? NSTextAlignmentRight : NSTextAlignmentLeft);
	if ([v isKindOfClass:NSTextField.class]) ((NSTextField *)v).alignment = al;
}

void ui_on(int ident, int on) {
	NSView *v = viewFor(ident);
	int k = kindOf(ident);
	syncing = YES;
	if (k == UI_CHIP) {
		NSButton *b = (NSButton *)v;
		b.bezelColor = on ? NSColor.controlAccentColor : nil;
		b.keyEquivalent = @"";
		b.font = on ? [NSFont systemFontOfSize:13 weight:NSFontWeightSemibold] : [NSFont systemFontOfSize:13];
	} else if (k == UI_CHECK) {
		((NSButton *)v).state = on ? NSControlStateValueOn : NSControlStateValueOff;
	} else if (k == UI_TOAST) {
		NSBox *b = (NSBox *)v;
		b.fillColor = on ? [NSColor.systemRedColor colorWithAlphaComponent:0.10] : [NSColor.labelColor colorWithAlphaComponent:0.06];
		b.borderColor = on ? [NSColor.systemRedColor colorWithAlphaComponent:0.35] : NSColor.separatorColor;
	}
	syncing = NO;
}

void ui_enabled(int ident, int on) {
	NSView *v = viewFor(ident);
	if ([v isKindOfClass:NSControl.class]) ((NSControl *)v).enabled = on != 0;
	else v.alphaValue = on ? 1 : 0.5;
}

void ui_hidden(int ident, int hidden) { viewFor(ident).hidden = hidden != 0; }

void ui_items(int ident, const char *items, int selected) {
	NSView *v = viewFor(ident);
	NSString *key = str(items);
	BOOL same = [itemsCache[@(ident)] isEqualToString:key];
	syncing = YES;
	if ([v isKindOfClass:NSPopUpButton.class]) {
		NSPopUpButton *p = (NSPopUpButton *)v;
		if (!same) {
			[p removeAllItems];
			NSArray *ls = lines(items);
			if (p.pullsDown) [p addItemWithTitle:ls.count ? ls[0] : @""]; // 下拉菜单的第一项是标题
			for (NSUInteger i = p.pullsDown ? 1 : 0; i < ls.count; i++) {
				if ([ls[i] isEqualToString:@"-"]) [p.menu addItem:NSMenuItem.separatorItem];
				else {
					[p addItemWithTitle:@"x"]; // 先占位再改名，免得同名的项被合并
					p.lastItem.title = ls[i];
				}
			}
		}
		if (!p.pullsDown && selected >= 0 && selected < p.numberOfItems && p.indexOfSelectedItem != selected) [p selectItemAtIndex:selected];
	} else if ([v isKindOfClass:NSSegmentedControl.class]) {
		NSSegmentedControl *s = (NSSegmentedControl *)v;
		if (!same) {
			NSArray *ls = lines(items);
			s.segmentCount = ls.count;
			for (NSUInteger i = 0; i < ls.count; i++) {
				[s setLabel:ls[i] forSegment:i];
				[s setWidth:0 forSegment:i];
			}
		}
		if (selected >= 0 && selected < s.segmentCount) s.selectedSegment = selected;
		else s.selectedSegment = -1;
	}
	itemsCache[@(ident)] = key;
	syncing = NO;
}

void ui_symbol(int ident, const char *name, double size) {
	NSView *v = viewFor(ident);
	NSImage *img = symbol(str(name), size, NSFontWeightRegular);
	if ([v isKindOfClass:NSImageView.class]) ((NSImageView *)v).image = img;
	else if ([v isKindOfClass:NSButton.class]) {
		NSButton *b = (NSButton *)v;
		b.image = img;
		b.imagePosition = img ? NSImageLeading : NSNoImage;
		b.imageHugsTitle = YES;
	}
}

void ui_tooltip(int ident, const char *s) { viewFor(ident).toolTip = s && *s ? str(s) : nil; }

void ui_fit(int ident, double *w, double *h) {
	NSView *v = viewFor(ident);
	NSSize s = v.fittingSize;
	if ([v isKindOfClass:NSControl.class]) {
		NSSize c = ((NSControl *)v).intrinsicContentSize;
		if (c.width > 0) s.width = c.width;
		if (c.height > 0) s.height = c.height;
	}
	*w = ceil(s.width);
	*h = ceil(s.height);
}

void ui_measure(const char *s, int font, double maxWidth, double *w, double *h) {
	NSString *t = str(s);
	NSDictionary *attrs = @{NSFontAttributeName : fontFor(font)};
	NSRect r = [t boundingRectWithSize:NSMakeSize(maxWidth > 0 ? maxWidth : CGFLOAT_MAX, CGFLOAT_MAX)
	                           options:NSStringDrawingUsesLineFragmentOrigin | NSStringDrawingUsesFontLeading
	                        attributes:attrs];
	*w = ceil(r.size.width);
	*h = ceil(r.size.height);
}

void ui_doc_height(int scroll, double h) {
	NSScrollView *s = (NSScrollView *)viewFor(scroll);
	if (![s isKindOfClass:NSScrollView.class]) return;
	NSSize c = s.contentSize;
	NSRect r = NSMakeRect(0, 0, c.width, MAX(h, c.height));
	if (!NSEqualRects(s.documentView.frame, r)) s.documentView.frame = r;
}

double ui_doc_width(int scroll) {
	NSScrollView *s = (NSScrollView *)viewFor(scroll);
	if (![s isKindOfClass:NSScrollView.class]) return 0;
	return s.contentSize.width;
}

// ---------------------------------------------------------------- 分类和文件列表

void ui_sidebar(const char *titles, const char *syms, const char *badges, int selected) {
	navTitles = lines(titles);
	navSymbols = lines(syms);
	navBadges = [str(badges) componentsSeparatedByString:@"\n"];
	syncing = YES;
	[navTable reloadData];
	if (selected >= 0) [navTable selectRowIndexes:[NSIndexSet indexSetWithIndex:selected] byExtendingSelection:NO];
	syncing = NO;
}

void ui_set_version(const char *s) { verLabel.stringValue = str(s); }

void ui_list_reload(void) {
	[rowCache removeAllObjects];
	[listTable reloadData];
}

void ui_list_refresh(void) {
	[rowCache removeAllObjects];
	NSInteger cols = listTable.numberOfColumns;
	[listTable enumerateAvailableRowViewsUsingBlock:^(NSTableRowView *rv, NSInteger row) {
		if (row < 0 || row >= listTable.numberOfRows) return;
		OCRowData *d = rowData(row);
		for (NSInteger c = 0; c < cols; c++) {
			NSTableCellView *cell = [rv viewAtColumn:c];
			if ([cell isKindOfClass:NSTableCellView.class]) configureCell(cell, listTable.tableColumns[c].identifier, d);
		}
	}];
}

int ui_list_selection(int *rows, int max) {
	__block int n = 0;
	[listTable.selectedRowIndexes enumerateIndexesUsingBlock:^(NSUInteger i, BOOL *stop) {
		if (n >= max) {
			*stop = YES;
			return;
		}
		rows[n++] = (int)i;
	}];
	return n;
}

void ui_list_select(int row) {
	if (row < 0 || row >= listTable.numberOfRows) {
		[listTable deselectAll:nil];
		return;
	}
	[listTable selectRowIndexes:[NSIndexSet indexSetWithIndex:row] byExtendingSelection:NO];
	[listTable scrollRowToVisible:row];
}

// ---------------------------------------------------------------- 对话框

int ui_alert(int style, const char *title, const char *msg, const char *detail, const char *buttons) {
	NSAlert *a = [NSAlert new];
	a.alertStyle = style == 2 ? NSAlertStyleCritical : (style == 1 ? NSAlertStyleWarning : NSAlertStyleInformational);
	a.messageText = str(title);
	a.informativeText = str(msg);
	for (NSString *b in lines(buttons)) [a addButtonWithTitle:b];
	if (a.buttons.count == 0) [a addButtonWithTitle:@"好"];
	if (a.buttons.count > 1 && [a.buttons.lastObject.title isEqualToString:@"取消"]) a.buttons.lastObject.keyEquivalent = @"\033";
	NSString *d = str(detail);
	if (d.length > 0) {
		// 技术细节放在一个可以滚动、可以拷贝的小文本框里
		NSScrollView *sv = [[NSScrollView alloc] initWithFrame:NSMakeRect(0, 0, 380, 110)];
		sv.hasVerticalScroller = YES;
		sv.borderType = NSBezelBorder;
		NSTextView *tv = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, 380, 110)];
		tv.editable = NO;
		tv.font = [NSFont monospacedSystemFontOfSize:11 weight:NSFontWeightRegular];
		tv.string = d;
		tv.textContainerInset = NSMakeSize(4, 4);
		tv.autoresizingMask = NSViewWidthSizable;
		sv.documentView = tv;
		a.accessoryView = sv;
	}
	if (style == 3) a.icon = [NSApp applicationIconImage];
	NSModalResponse r = [a runModal];
	return (int)(r - NSAlertFirstButtonReturn);
}

char *ui_open_panel(const char *title, const char *exts) {
	NSOpenPanel *p = [NSOpenPanel openPanel];
	p.message = str(title);
	p.canChooseFiles = YES;
	p.canChooseDirectories = YES;
	p.allowsMultipleSelection = YES;
	p.prompt = @"添加";
	NSMutableArray<UTType *> *types = [NSMutableArray array];
	for (NSString *e in lines(exts)) {
		UTType *t = [UTType typeWithFilenameExtension:e];
		if (t) [types addObject:t];
	}
	if (types.count) p.allowedContentTypes = types;
	if ([p runModal] != NSModalResponseOK) return NULL;
	NSMutableArray *paths = [NSMutableArray array];
	for (NSURL *u in p.URLs)
		if (u.path) [paths addObject:u.path];
	return cstr([paths componentsJoinedByString:@"\n"]);
}

char *ui_choose_folder(const char *title, const char *start) {
	NSOpenPanel *p = [NSOpenPanel openPanel];
	p.message = str(title);
	p.canChooseFiles = NO;
	p.canChooseDirectories = YES;
	p.canCreateDirectories = YES;
	p.allowsMultipleSelection = NO;
	p.prompt = @"选择";
	if (start && *start) p.directoryURL = [NSURL fileURLWithPath:str(start) isDirectory:YES];
	if ([p runModal] != NSModalResponseOK || p.URL.path == nil) return NULL;
	return cstr(p.URL.path);
}

void ui_open_path(const char *path) { [NSWorkspace.sharedWorkspace openURL:[NSURL fileURLWithPath:str(path)]]; }

void ui_reveal(const char *path) { [NSWorkspace.sharedWorkspace activateFileViewerSelectingURLs:@[ [NSURL fileURLWithPath:str(path)] ]]; }

void ui_open_url(const char *url) {
	NSURL *u = [NSURL URLWithString:str(url)];
	if (u) [NSWorkspace.sharedWorkspace openURL:u];
}

char *ui_paste_files(void) {
	NSArray *files = filesFrom(NSPasteboard.generalPasteboard);
	if (files.count == 0) return NULL;
	return cstr([files componentsJoinedByString:@"\n"]);
}

void ui_copy_text(const char *s) {
	NSPasteboard *pb = NSPasteboard.generalPasteboard;
	[pb clearContents];
	[pb setString:str(s) forType:NSPasteboardTypeString];
}

void ui_post(int what) {
	dispatch_async(dispatch_get_main_queue(), ^{
		goPosted(what);
	});
}

void ui_timer(int which, double seconds) {
	ui_timer_stop(which);
	NSTimer *t = [NSTimer timerWithTimeInterval:seconds target:app selector:@selector(timerFired:) userInfo:@(which) repeats:YES];
	[NSRunLoop.mainRunLoop addTimer:t forMode:NSRunLoopCommonModes];
	timers[@(which)] = t;
}

void ui_timer_stop(int which) {
	[timers[@(which)] invalidate];
	[timers removeObjectForKey:@(which)];
}

void ui_keep_awake(int on) {
	if (on && !activity) {
		activity = [NSProcessInfo.processInfo beginActivityWithOptions:NSActivityUserInitiated | NSActivityIdleSystemSleepDisabled
		                                                        reason:@"正在转换文件"];
	} else if (!on && activity) {
		[NSProcessInfo.processInfo endActivity:activity];
		activity = nil;
	}
}

// 程序坞图标：转换时在图标下面显示总进度，右上角显示还剩几个
void ui_dock(double frac, const char *badge) {
	NSDockTile *tile = NSApp.dockTile;
	static NSProgressIndicator *bar;
	if (frac < 0) {
		tile.contentView = nil;
		tile.badgeLabel = nil;
		[tile display];
		return;
	}
	if (!tile.contentView) {
		NSImageView *iv = [NSImageView imageViewWithImage:NSApp.applicationIconImage];
		iv.frame = NSMakeRect(0, 0, tile.size.width, tile.size.height);
		bar = [[NSProgressIndicator alloc] initWithFrame:NSMakeRect(tile.size.width * 0.1, tile.size.height * 0.06, tile.size.width * 0.8, 14)];
		bar.style = NSProgressIndicatorStyleBar;
		bar.indeterminate = NO;
		bar.minValue = 0;
		bar.maxValue = 1;
		[iv addSubview:bar];
		tile.contentView = iv;
	}
	bar.doubleValue = frac;
	tile.badgeLabel = badge && *badge ? str(badge) : nil;
	[tile display];
}

void ui_attention(void) {
	if (!NSApp.active) [NSApp requestUserAttention:NSInformationalRequest];
}

void ui_free(char *p) { free(p); }

// ---------------------------------------------------------------- 截图模式

void ui_pump(double seconds) {
	NSDate *until = [NSDate dateWithTimeIntervalSinceNow:seconds];
	while ([until timeIntervalSinceNow] > 0) {
		@autoreleasepool {
			NSEvent *e = [NSApp nextEventMatchingMask:NSEventMaskAny untilDate:[NSDate dateWithTimeIntervalSinceNow:0.02] inMode:NSDefaultRunLoopMode dequeue:YES];
			if (e) [NSApp sendEvent:e];
			[NSRunLoop.currentRunLoop runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.005]];
		}
	}
}

void ui_dark(int dark) { NSApp.appearance = [NSAppearance appearanceNamed:dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua]; }

void ui_window_size(double w, double h) {
	NSRect f = win.frame;
	NSRect c = [win contentRectForFrameRect:f];
	c.size = NSMakeSize(w, h);
	[win setFrame:[win frameRectForContentRect:c] display:YES];
}

// 把整个窗口（含标题栏的红黄绿按钮）画成 2 倍分辨率的 PNG，不需要「屏幕录制」权限
int ui_save_png(const char *path) {
	NSView *v = root.superview ?: root;
	[v layoutSubtreeIfNeeded];
	[v displayIfNeeded];
	NSRect b = v.bounds;
	NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL
	                                                               pixelsWide:(NSInteger)(b.size.width * 2)
	                                                               pixelsHigh:(NSInteger)(b.size.height * 2)
	                                                            bitsPerSample:8
	                                                          samplesPerPixel:4
	                                                                 hasAlpha:YES
	                                                                 isPlanar:NO
	                                                           colorSpaceName:NSCalibratedRGBColorSpace
	                                                              bytesPerRow:0
	                                                             bitsPerPixel:0];
	rep.size = b.size;
	[v cacheDisplayInRect:b toBitmapImageRep:rep];
	NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
	BOOL ok = [png writeToFile:str(path) atomically:YES];
	// 顺便试试系统截图（只截这个窗口）；没有权限时什么也不会发生
	NSString *sc = [[str(path) stringByDeletingPathExtension] stringByAppendingString:@"-screencapture.png"];
	NSTask *t = [NSTask new];
	t.launchPath = @"/usr/sbin/screencapture";
	t.arguments = @[ @"-x", @"-o", [NSString stringWithFormat:@"-l%ld", (long)win.windowNumber], sc ];
	@try {
		[t launch];
		[t waitUntilExit];
	} @catch (NSException *e) {
	}
	return ok ? 1 : 0;
}
