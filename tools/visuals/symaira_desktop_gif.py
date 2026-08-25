from manim import *

# GitHub-friendly 16:9 explainer: dark Symaira palette, no external assets.
config.pixel_width = 1920
config.pixel_height = 1080
config.frame_rate = 30
config.background_color = "#090908"

BG = "#090908"
PANEL = "#161512"
PANEL_2 = "#1D1A15"
GRID = "#2D281E"
CREAM = "#F8F0DC"
MUTED = "#B7AD95"
GOLD = "#D6A85B"
AMBER = "#F0C36B"
BLUE = "#8EC9FF"
GREEN = "#9CD6AD"
RED = "#E99A88"


def tx(value, size=24, color=CREAM, font="Avenir Next"):
    return Text(value, font=font, font_size=size, color=color)


def mono(value, size=18, color=MUTED):
    return Text(value, font="Menlo", font_size=size, color=color)


def fit(mobject, max_width, max_height=None):
    """Keep text inside a visual slot without distorting its proportions."""
    if mobject.width > max_width:
        mobject.scale_to_fit_width(max_width)
    if max_height is not None and mobject.height > max_height:
        mobject.scale_to_fit_height(max_height)
    return mobject


def panel(width, height, stroke=GOLD, fill=PANEL, opacity=1.0, radius=0.16):
    return RoundedRectangle(
        width=width,
        height=height,
        corner_radius=radius,
        stroke_color=stroke,
        stroke_width=1.6,
        fill_color=fill,
        fill_opacity=opacity,
    )


def card(title, subtitle, width=2.65, height=1.05, accent=GOLD, subtitle_color=MUTED):
    box = panel(width, height, stroke=accent, fill=PANEL)
    title_m = fit(tx(title, 17, CREAM), width - 0.34, 0.27)
    subtitle_m = fit(tx(subtitle, 10, subtitle_color), width - 0.34, 0.18)
    title_m.move_to(box.get_center() + UP * 0.14)
    subtitle_m.move_to(box.get_center() + DOWN * 0.22)
    return VGroup(box, title_m, subtitle_m)


def pill(label, accent=GOLD, width=None):
    label_m = fit(tx(label, 10.5, CREAM), (width or 1.2) - 0.22, 0.17)
    width = width or max(0.9, label_m.width + 0.30)
    box = RoundedRectangle(
        width=width,
        height=0.30,
        corner_radius=0.12,
        stroke_color=accent,
        stroke_width=1.0,
        fill_color=accent,
        fill_opacity=0.12,
    )
    label_m.move_to(box.get_center())
    return VGroup(box, label_m)


def arrow(start, end, color=MUTED):
    return Arrow(
        start,
        end,
        buff=0.10,
        stroke_color=color,
        stroke_width=2.1,
        max_tip_length_to_length_ratio=0.14,
    )


def background_grid():
    dots = VGroup()
    for x in np.arange(-6.8, 6.9, 0.55):
        for y in np.arange(-3.6, 3.7, 0.55):
            dots.add(Dot([x, y, 0], radius=0.008, color=GRID, fill_opacity=0.65))
    return dots


class SymairaDesktopExplainer(Scene):
    def construct(self):
        self.camera.background_color = BG
        grid = background_grid()
        self.add(grid)

        # Opening: product identity.
        eyebrow = mono("SYMAIRA  /  OPEN LOCAL TOOLS", 13, GOLD)
        title = tx("Symaira Desktop", 44, CREAM)
        subtitle = tx("Markdown-native workspace for documents and AI", 20, MUTED)
        rule = Line(LEFT * 2.7, RIGHT * 2.7, color=GOLD, stroke_width=1.3)
        badges = VGroup(
            pill("Markdown SSOT", GOLD, 1.45),
            pill("OCR", AMBER, 0.80),
            pill("MCP", BLUE, 0.80),
            pill("Self-hosted", GREEN, 1.10),
        ).arrange(RIGHT, buff=0.16)
        opener = VGroup(eyebrow, title, subtitle, rule, badges).arrange(DOWN, buff=0.20)
        opener.move_to(ORIGIN)

        self.play(FadeIn(eyebrow, shift=UP * 0.15), run_time=0.45)
        self.play(Write(title), FadeIn(subtitle, shift=UP * 0.08), run_time=0.75)
        self.play(GrowFromCenter(rule), LaggedStart(*[FadeIn(b, shift=UP * 0.08) for b in badges], lag_ratio=0.10), run_time=0.75)
        self.wait(0.25)

        # Architecture view.
        self.play(FadeOut(opener, shift=UP * 0.18), run_time=0.35)
        arch_title = tx("One core, many surfaces", 28, CREAM).to_edge(UP, buff=0.38)
        arch_subtitle = tx("The vault stays open. The interfaces stay replaceable.", 14, MUTED).next_to(arch_title, DOWN, buff=0.10)
        self.play(FadeIn(arch_title, shift=DOWN * 0.12), FadeIn(arch_subtitle), run_time=0.30)

        core = card("symdesk core", "shared service layer", width=3.15, height=1.22, accent=AMBER)
        core.move_to(ORIGIN + RIGHT * 0.05 + DOWN * 0.05)
        core_tag = mono("CLI  ·  MCP  ·  API", 12, AMBER).next_to(core, DOWN, buff=0.08)

        native = card("Native apps", "macOS · iOS", width=2.45, height=0.92, accent=BLUE)
        native.move_to(LEFT * 4.25 + UP * 1.20)
        agents = card("AI agents", "MCP · CLI", width=2.45, height=0.92, accent=GREEN)
        agents.move_to(LEFT * 4.25 + DOWN * 1.20)
        server = card("Self-hosted", "API + OCR workers", width=2.45, height=0.92, accent=GOLD)
        server.move_to(RIGHT * 4.25 + UP * 1.20)
        vault = card("Markdown vault", "source of truth", width=2.45, height=0.92, accent=CREAM, subtitle_color=CREAM)
        vault.move_to(RIGHT * 4.25 + DOWN * 0.06)
        sidecar = card("SQLite sidecar", "FTS5 index", width=2.45, height=0.92, accent=GOLD)
        sidecar.move_to(RIGHT * 4.25 + DOWN * 1.32)

        left_arrows = VGroup(arrow(native.get_right(), core.get_left() + UP * 0.30, BLUE), arrow(agents.get_right(), core.get_left() + DOWN * 0.30, GREEN))
        right_arrows = VGroup(arrow(core.get_right() + UP * 0.30, server.get_left(), GOLD), arrow(core.get_right(), vault.get_left(), CREAM), arrow(core.get_right() + DOWN * 0.30, sidecar.get_left(), GOLD))

        self.play(FadeIn(core, scale=0.94), FadeIn(core_tag), run_time=0.55)
        self.play(FadeIn(native, shift=RIGHT * 0.12), FadeIn(agents, shift=RIGHT * 0.12), Create(left_arrows), run_time=0.65)
        self.play(FadeIn(server, shift=LEFT * 0.12), FadeIn(vault, shift=LEFT * 0.12), FadeIn(sidecar, shift=LEFT * 0.12), Create(right_arrows), run_time=0.85)
        self.wait(0.70)

        # Highlight the source of truth and derived index relationship.
        pulse = SurroundingRectangle(vault, color=CREAM, stroke_width=2.8, buff=0.06)
        index_note = mono("derived · rebuildable", 12, GOLD).next_to(sidecar, DOWN, buff=0.08)
        self.play(Create(pulse), FadeIn(index_note), run_time=0.45)
        self.play(pulse.animate.set_stroke(opacity=0.25), run_time=0.55)
        self.play(FadeOut(pulse), FadeOut(index_note), run_time=0.25)

        arch_group = VGroup(arch_title, arch_subtitle, core, core_tag, native, agents, server, vault, sidecar, left_arrows, right_arrows)
        self.play(FadeOut(arch_group, shift=UP * 0.15), run_time=0.65)

        # Feature cards.
        feature_title = tx("What it gives you", 30, CREAM).to_edge(UP, buff=0.50)
        feature_subtitle = tx("A human shell for the Symaira ecosystem — local by default.", 15, MUTED).next_to(feature_title, DOWN, buff=0.12)
        self.play(FadeIn(feature_title, shift=DOWN * 0.12), FadeIn(feature_subtitle), run_time=0.45)

        features = [
            ("Markdown first", "portable\nfiles", CREAM),
            ("Search that works", "FTS5 +\nhybrid search", GOLD),
            ("Ingest + OCR", "local\nOCR workers", AMBER),
            ("Agent-native", "MCP + CLI\ncomposable tools", BLUE),
        ]
        feature_cards = VGroup()
        for title_text, body_text, accent in features:
            box = panel(2.55, 1.55, stroke=accent, fill=PANEL_2)
            icon = Circle(radius=0.12, color=accent, fill_color=accent, fill_opacity=0.95, stroke_width=0)
            t = fit(tx(title_text, 16, CREAM), box.width - 0.78, 0.26)
            b = Paragraph(*body_text.split("\n"), alignment="left", font_size=12, color=MUTED, line_spacing=0.55)
            b = fit(b, box.width - 0.74, 0.42)
            icon.move_to(box.get_left() + RIGHT * 0.30 + UP * 0.47)
            t.next_to(icon, RIGHT, buff=0.12).align_to(box, UP).shift(DOWN * 0.30)
            b.next_to(t, DOWN, buff=0.16).align_to(t, LEFT)
            feature_cards.add(VGroup(box, icon, t, b))
        feature_cards.arrange_in_grid(rows=2, cols=2, buff=(0.28, 0.28))
        feature_cards.move_to(DOWN * 0.45)
        self.play(LaggedStart(*[FadeIn(c, shift=UP * 0.16) for c in feature_cards], lag_ratio=0.16), run_time=1.15)
        focus = SurroundingRectangle(feature_cards[0], color=CREAM, stroke_width=2.2, buff=0.08)
        self.play(Create(focus), run_time=0.35)
        self.play(focus.animate.move_to(feature_cards[1]), run_time=0.45)
        self.play(focus.animate.move_to(feature_cards[2]), run_time=0.45)
        self.play(focus.animate.move_to(feature_cards[3]), run_time=0.45)
        self.play(FadeOut(focus), run_time=0.25)
        self.wait(0.45)
        self.play(FadeOut(feature_title, feature_subtitle, feature_cards), run_time=0.60)

        # The end-to-end flow.
        flow_title = tx("From document to grounded action", 28, CREAM).to_edge(UP, buff=0.50)
        flow_subtitle = tx("One durable path — whether a person or an agent starts it.", 15, MUTED).next_to(flow_title, DOWN, buff=0.12)
        self.play(FadeIn(flow_title, shift=DOWN * 0.12), FadeIn(flow_subtitle), run_time=0.45)

        stages = [
            ("1", "Capture", "PDF · mail · note", BLUE),
            ("2", "Ingest", "OCR + metadata", AMBER),
            ("3", "Store", "Markdown + index", CREAM),
            ("4", "Use", "search · AI · export", GREEN),
        ]
        stage_groups = VGroup()
        for number, heading, detail, accent in stages:
            box = panel(2.55, 1.12, stroke=accent, fill=PANEL)
            num = tx(number, 24, accent)
            heading_m = fit(tx(heading, 16, CREAM), 1.55, 0.25)
            detail_m = fit(tx(detail, 10, MUTED), 1.72, 0.17)
            num.move_to(box.get_left() + RIGHT * 0.34)
            heading_m.next_to(num, RIGHT, buff=0.12).align_to(box, UP).shift(DOWN * 0.28)
            detail_m.next_to(heading_m, DOWN, buff=0.10).align_to(heading_m, LEFT)
            stage_groups.add(VGroup(box, num, heading_m, detail_m))
        stage_groups.arrange(RIGHT, buff=0.28).move_to(DOWN * 0.38)
        flow_arrows = VGroup(*[arrow(stage_groups[i].get_right(), stage_groups[i + 1].get_left(), MUTED) for i in range(3)])
        self.play(LaggedStart(*[FadeIn(s, shift=RIGHT * 0.12) for s in stage_groups], lag_ratio=0.15), Create(flow_arrows), run_time=1.0)

        cursor = Dot(stage_groups[0].get_top() + DOWN * 0.10, radius=0.075, color=AMBER)
        self.add(cursor)
        waypoints = [stage_groups[i].get_top() + DOWN * 0.10 for i in range(4)]
        for point in waypoints:
            self.play(cursor.animate.move_to(point), run_time=0.55, rate_func=smooth)
        self.play(cursor.animate.move_to(waypoints[0]), run_time=0.65, rate_func=smooth)
        self.remove(cursor)
        self.wait(0.35)

        # Closing frame; dark background keeps the GIF loop visually calm.
        ending = VGroup(
            mono("SYMAIRA  /  LOCAL · OPEN · COMPOSABLE", 13, GOLD),
            tx("One surface. Open files. Local control.", 30, CREAM),
            tx("Symaira Desktop", 18, MUTED),
        ).arrange(DOWN, buff=0.20)
        self.play(FadeOut(flow_title, flow_subtitle, stage_groups, flow_arrows), run_time=0.55)
        self.play(FadeIn(ending, shift=UP * 0.12), run_time=0.65)
        self.wait(0.80)
        self.play(FadeOut(ending), run_time=0.55)
        self.wait(0.20)
