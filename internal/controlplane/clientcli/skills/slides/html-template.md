# HTML Presentation Template

Reference architecture for generating slide presentations. Every presentation follows a fixed 16:9 stage model: slides are authored at 1920×1080 and the whole stage scales to fit the browser window.

## Base HTML Structure

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Presentation Title</title>

    <!-- Fonts: system font stacks only (STYLE_PRESETS.md); no <link> to any host -->

    <style>
        /* ===========================================
           CSS CUSTOM PROPERTIES (THEME)
           Change these to change the whole look
           =========================================== */
        :root {
            /* Colors — from chosen style preset */
            --bg-primary: #0a0f1c;
            --bg-secondary: #111827;
            --text-primary: #ffffff;
            --text-secondary: #9ca3af;
            --accent: #00ffcc;
            --accent-glow: rgba(0, 255, 204, 0.3);

            /* Typography — authored at 1920×1080 stage size */
            --font-display: Bahnschrift, 'DIN Alternate', 'Franklin Gothic Medium', 'Nimbus Sans Narrow', sans-serif-condensed, sans-serif;
            --font-body: Avenir, Montserrat, Corbel, 'URW Gothic', source-sans-pro, sans-serif;
            --title-size: 112px;
            --subtitle-size: 34px;
            --body-size: 28px;

            /* Spacing — authored at 1920×1080 stage size */
            --slide-padding: 72px;
            --content-gap: 32px;

            /* Animation */
            --ease-out-expo: cubic-bezier(0.16, 1, 0.3, 1);
            --duration-normal: 0.6s;
        }

        /* ===========================================
           BASE STYLES
           =========================================== */
        * { margin: 0; padding: 0; box-sizing: border-box; }

        /* --- PASTE viewport-base.css CONTENTS HERE --- */

        /* ===========================================
           ANIMATIONS
           Trigger via .visible class on the active slide
           =========================================== */
        .reveal {
            opacity: 0;
            transform: translateY(30px);
            transition: opacity var(--duration-normal) var(--ease-out-expo),
                        transform var(--duration-normal) var(--ease-out-expo);
        }

        .slide.visible .reveal {
            opacity: 1;
            transform: translateY(0);
        }

        /* Stagger children for sequential reveal */
        .reveal:nth-child(1) { transition-delay: 0.1s; }
        .reveal:nth-child(2) { transition-delay: 0.2s; }
        .reveal:nth-child(3) { transition-delay: 0.3s; }
        .reveal:nth-child(4) { transition-delay: 0.4s; }

        /* ... preset-specific styles ... */
    </style>
</head>
<body>
    <div class="deck-viewport">
        <main class="deck-stage" id="deckStage">
            <section class="slide title-slide active">
                <h1 class="reveal">Presentation Title</h1>
                <p class="reveal">Subtitle or author</p>
            </section>

            <section class="slide">
                <div class="slide-content">
                    <h2 class="reveal">Slide Title</h2>
                    <p class="reveal">Content...</p>
                </div>
            </section>

            <!-- More slides... -->
        </main>
    </div>

    <script>
        /* ===========================================
           SLIDE PRESENTATION CONTROLLER
           =========================================== */
        class SlidePresentation {
            constructor() {
                this.slides = document.querySelectorAll('.slide');
                this.currentSlide = 0;
                this.stage = document.getElementById('deckStage');
                this.setupStageScale();
                this.setupKeyboardNav();
                this.setupTouchNav();
                this.showSlide(0);
            }

            setupStageScale() {
                const scale = () => {
                    const factor = Math.min(window.innerWidth / 1920, window.innerHeight / 1080);
                    const x = (window.innerWidth - 1920 * factor) / 2;
                    const y = (window.innerHeight - 1080 * factor) / 2;
                    this.stage.style.transform = `translate(${x}px, ${y}px) scale(${factor})`;
                };
                scale();
                window.addEventListener('resize', scale);
            }

            setupKeyboardNav() {
                // Arrow keys, Space, Page Up/Down
            }

            setupTouchNav() {
                // Touch/swipe support for mobile
            }

            showSlide(index) {
                this.currentSlide = Math.max(0, Math.min(index, this.slides.length - 1));
                this.slides.forEach((slide, i) => {
                    slide.classList.toggle('active', i === this.currentSlide);
                    slide.classList.toggle('visible', i === this.currentSlide);
                });
            }
        }

        new SlidePresentation();
    </script>
</body>
</html>
```

## Required JavaScript Features

Every presentation must include:

1. **SlidePresentation Class** — Main controller with:
   - Keyboard navigation (arrows, space, page up/down, Home/End), listened for on `document`
   - Touch/swipe support
   - Mouse wheel navigation
   - Optional progress indicator or page count, kept outside the slide stage

   The deck runs in the artifact's frame: it gets the keys once the owner clicks it, so a click on the stage must
   not do anything else. Do not call `requestFullscreen` (the frame may not); the panel has its own Full screen.

2. **Stage Scaling** — For fixed 16:9 presentation behavior:
   - Keep all slides at 1920×1080 inside `.deck-stage`
   - Scale the whole stage with one transform
   - Letterbox/pillarbox as needed; never reflow slide content per device

3. **Optional Enhancements** (match to chosen style):
   - Custom cursor with trail
   - Particle system background (canvas)
   - Parallax effects
   - 3D tilt on hover
   - Magnetic buttons
   - Counter animations

No inline editing and no `localStorage`, `sessionStorage` or cookies: the frame has no storage and saves no file.

## Image Pipeline (Skip If No Images)

If user chose "No images" in Phase 1, skip this entirely. If images were provided, process them before generating HTML.

**Dependency:** Pillow, in `python` (the documents environment, or the one the `pdf` skill's Prerequisites make on a worker); never `pip install` into the documents environment.

### Image Processing

```python
from PIL import Image, ImageDraw

# Circular crop (for logos on modern/clean styles)
def crop_circle(input_path, output_path):
    img = Image.open(input_path).convert('RGBA')
    w, h = img.size
    size = min(w, h)
    left, top = (w - size) // 2, (h - size) // 2
    img = img.crop((left, top, left + size, top + size))
    mask = Image.new('L', (size, size), 0)
    ImageDraw.Draw(mask).ellipse([0, 0, size, size], fill=255)
    img.putalpha(mask)
    img.save(output_path, 'PNG')

# Resize (for oversized images that inflate HTML)
def resize_max(input_path, output_path, max_dim=1200):
    img = Image.open(input_path)
    img.thumbnail((max_dim, max_dim), Image.LANCZOS)
    img.save(output_path, quality=85)
```

| Situation | Operation |
|-----------|-----------|
| Square logo on rounded aesthetic | `crop_circle()` |
| Image > 1MB | `resize_max(max_dim=1200)` |
| Wrong aspect ratio | Manual crop with `img.crop()` |

Save processed images with `_processed` suffix. Never overwrite originals.

### Image Placement

**Use relative file paths** (not base64) in a deck folder shown as a folder, whose images are served beside its
`index.html`; only a small logo goes into a single-file deck as a `data:` URL. Never an absolute path or another
host:

```html
<img src="assets/logo_round.png" alt="Logo" class="slide-image logo">
<img src="assets/screenshot.png" alt="Screenshot" class="slide-image screenshot">
```

```css
.slide-image {
    max-width: 100%;
    max-height: min(50vh, 400px);
    object-fit: contain;
    border-radius: 8px;
}
.slide-image.screenshot {
    border: 1px solid rgba(255, 255, 255, 0.1);
    border-radius: 12px;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
}
.slide-image.logo {
    max-height: min(30vh, 200px);
}
```

**Adapt border/shadow colors to match the chosen style's accent.** Never repeat the same image on multiple slides (except logos on title + closing).

**Placement patterns:** Logo centered on title slide. Screenshots in two-column layouts with text. Full-bleed images as slide backgrounds with text overlay (use sparingly).

---

## Code Quality

**Comments:** Every section needs clear comments explaining what it does and how to modify it.

**Accessibility:**
- Semantic HTML (`<section>`, `<nav>`, `<main>`)
- Keyboard navigation works fully
- ARIA labels where needed
- `prefers-reduced-motion` support (included in viewport-base.css)

## File Structure

A presentation without images (one HTML file, at most 256 KB):
```
[name].html          # Self-contained, all CSS/JS inline
```

A presentation with images (the folder `[name]`, at most 5 MB in all):
```
[name]/index.html    # Self-contained, all CSS/JS inline
[name]/assets/       # Images only
```
