(function () {
  function init(container) {
    container.innerHTML = `
      <div class="regions-page">
        <div class="regions-hero">
          <h1 class="regions-title">MeshCore Regions</h1>
          <p class="regions-subtitle">A guide to configuring regional packet filtering on SWBC / Salish Mesh</p>
        </div>

        <div class="regions-content">

          <section class="regions-section">
            <h2>What are Regions?</h2>
            <p>MeshCore Regions allow repeaters to selectively forward packets based on a named geographic or logical area. When a packet is scoped to a region, a repeater will only flood it if the repeater has that region configured with flood permissions enabled. Every repeater along the path must have matching region configuration for end-to-end forwarding to work.</p>
            <p>SWBC / Salish Mesh repeaters are currently configured with (at minimum):</p>
            <ul>
              <li><strong>Allow Flood</strong> for <code class="regions-code">*</code> — packets without a region scope are flooded normally</li>
              <li><strong>Allow Flood</strong> for region <code class="regions-code">bc</code> — packets scoped to <code class="regions-code">bc</code> are flooded normally</li>
            </ul>
          </section>

          <div class="regions-role-toggle">
            <button class="regions-role-btn active" data-role="companion" id="roleCompanionBtn">
              <span class="regions-role-icon">📱</span> Companion
            </button>
            <button class="regions-role-btn" data-role="repeater" id="roleRepeaterBtn">
              <span class="regions-role-icon">📡</span> Repeater
            </button>
          </div>

          <div class="regions-role-panel" id="regionsPanelCompanion" data-panel="companion">

            <section class="regions-section">
              <h2>Option A — Companion on Firmware 1.15 (Recommended)</h2>
              <p>Firmware 1.15 introduces a <strong>Default Scope</strong> setting. Once set, all flood packets your companion sends — including adverts, DMs, and login requests — will automatically be scoped to your chosen region.</p>

              <div class="regions-steps">
                <div class="regions-step">
                  <div class="regions-step-num">1</div>
                  <div>
                    <div class="regions-step-title">Flash firmware 1.15</div>
                    <div class="regions-step-body">Flash the standard 1.15 firmware for your device using the <a href="https://meshcore.io/flasher" target="_blank" rel="noopener" class="regions-link">MeshCore Web Flasher</a>.</div>
                  </div>
                </div>

                <div class="regions-step">
                  <div class="regions-step-num">2</div>
                  <div>
                    <div class="regions-step-title">Open Experimental Settings</div>
                    <div class="regions-step-body">In the MeshCore app (v1.43.0+), tap the gear icon → <strong>Experimental Settings</strong>.</div>
                    <img src="images/Experimental Location.jpg" alt="Experimental Settings menu location" class="regions-screenshot">
                  </div>
                </div>

                <div class="regions-step">
                  <div class="regions-step-num">3</div>
                  <div>
                    <div class="regions-step-title">Set Default Scope to <code class="regions-code">bc</code></div>
                    <div class="regions-step-body">Enter <code class="regions-code">bc</code> in the Default Scope field and save. All outgoing flood packets will now be scoped to the BC region.</div>
                    <img src="images/Default scope.jpg" alt="Default Scope field in Experimental Settings" class="regions-screenshot">
                  </div>
                </div>
              </div>
            </section>

            <section class="regions-section">
              <h2>Option B — Per-channel Scope on Firmware 1.14.1</h2>
              <p>If you're not ready to upgrade to 1.15, you can scope individual group channels instead.</p>

              <div class="regions-steps">
                <div class="regions-step">
                  <div class="regions-step-num">1</div>
                  <div>
                    <div class="regions-step-title">Open a group channel</div>
                    <div class="regions-step-body">In the MeshCore app, navigate to the group channel you want to scope.</div>
                  </div>
                </div>

                <div class="regions-step">
                  <div class="regions-step-num">2</div>
                  <div>
                    <div class="regions-step-title">Set Scope</div>
                    <div class="regions-step-body">Tap the channel menu and select <strong>Set Scope</strong>. Enter <code class="regions-code">bc</code> and save. This scope applies to messages sent in this channel only.</div>
                    <img src="images/set scope 1.14.1.jpg" alt="Set Scope option in channel menu" class="regions-screenshot">
                    <img src="images/set scope 1.14.1 success.jpg" alt="Set Scope saved successfully" class="regions-screenshot">
                    <img src="images/1.14.1 success.jpg" alt="Firmware 1.14.1 success" class="regions-screenshot">
                  </div>
                </div>

                <div class="regions-step">
                  <div class="regions-step-num">3</div>
                  <div>
                    <div class="regions-step-title">Repeat for each channel</div>
                    <div class="regions-step-body">Repeat for any other group channels you use. Note: direct messages and adverts are not scoped with this method — upgrading to 1.15 with a default scope is recommended for full coverage.</div>
                  </div>
                </div>
              </div>
            </section>

          </div>

          <div class="regions-role-panel regions-role-panel--hidden" id="regionsPanelRepeater" data-panel="repeater">

            <section class="regions-section">
              <h2>Repeater Setup</h2>
              <p>If you operate a repeater on SWBC / Salish Mesh, please add the <code class="regions-code">bc</code> region with <strong>Allow Flood</strong> enabled — this ensures your repeater forwards packets scoped to that region.</p>

              <div class="regions-subsection">
                <h3>Via the App UI</h3>
                <div class="regions-steps">
                  <div class="regions-step">
                    <div class="regions-step-num">1</div>
                    <div>
                      <div class="regions-step-title">Open Manage Regions</div>
                      <div class="regions-step-body">Connect to your repeater in the MeshCore app. Go to <strong>Settings → Manage Regions</strong>.</div>
                      <img src="images/repeater step 1.jpg" alt="Settings → Manage Regions" class="regions-screenshot">
                    </div>
                  </div>
                  <div class="regions-step">
                    <div class="regions-step-num">2</div>
                    <div>
                      <div class="regions-step-title">Add the <code class="regions-code">bc</code> region</div>
                      <div class="regions-step-body">Tap the <strong>3 dots</strong> in the top right corner, select <strong>Add Region</strong>, and enter <code class="regions-code">bc</code>.</div>
                      <img src="images/repeater step 2.jpg" alt="Adding bc region" class="regions-screenshot">
                    </div>
                  </div>
                  <div class="regions-step">
                    <div class="regions-step-num">3</div>
                    <div>
                      <div class="regions-step-title">Enable Allow Flood</div>
                      <div class="regions-step-body">If the new region is red and set to deny, tap the three-dot menu next to <code class="regions-code">bc</code> and select <strong>Allow Flood</strong>. Ensure you tap the <strong>check mark</strong> to save settings.</div>
                      <img src="images/repeater step 3a.jpg" alt="Allow Flood option" class="regions-screenshot">
                      <div class="regions-step-body" style="margin-top: 16px;">Repeater Regions should look similar to this — you can add more than just <code class="regions-code">bc</code>.</div>
                      <img src="images/repeater step 3b.jpg" alt="Repeater Regions saved" class="regions-screenshot">
                    </div>
                  </div>
                  <div class="regions-step">
                    <div class="regions-step-num">4</div>
                    <div>
                      <div class="regions-step-title">Set Default Scope <span style="font-weight:400;font-size:12px;color:var(--text-muted)">(v1.15+ · CLI only)</span></div>
                      <div class="regions-step-body">Optionally, set <code class="regions-code">bc</code> as the default scope for your repeater. This means the repeater's own packets (such as adverts) will be scoped to <code class="regions-code">bc</code> automatically. Run via serial CLI: <code class="regions-code">region default bc</code> — no <code class="regions-code">region save</code> needed, it saves automatically.</div>
                      <img src="images/rptr step 4.jpg" alt="Set default scope" class="regions-screenshot">
                    </div>
                  </div>
                </div>
              </div>

              <div class="regions-subsection">
                <h3>Via CLI (Serial)</h3>
                <p>Connect to your repeater via serial or the CLI function in the app and run the following commands:</p>
                <p style="margin-top:12px;font-weight:600;">Add the <code class="regions-code">bc</code> region (minimum setup):</p>
                <div class="regions-cli-block">
                  <div class="regions-cli-line"><code class="regions-code">region put bc</code> <span class="regions-cli-comment">— add the bc region (Allow Flood is the default)</span></div>
                  <div class="regions-cli-line"><code class="regions-code">region save</code> <span class="regions-cli-comment">— persist to storage</span></div>
                </div>
                <p style="margin-top:12px;font-weight:600;">Additional commands:</p>
                <div class="regions-cli-block">
                  <div class="regions-cli-line"><code class="regions-code">region allowf bc</code> <span class="regions-cli-comment">— explicitly enable flood for bc (if not already set)</span></div>
                  <div class="regions-cli-line"><code class="regions-code">region allowf *</code> <span class="regions-cli-comment">— allow flood for unscoped packets</span></div>
                  <div class="regions-cli-line"><code class="regions-code">region denyf *</code> <span class="regions-cli-comment">— deny flood for unscoped packets (do not set this currently)</span></div>
                  <div class="regions-cli-line"><code class="regions-code">region default bc</code> <span class="regions-cli-comment">— set bc as default scope for repeater's own packets (v1.15+, saves automatically)</span></div>
                </div>
                <p>To verify: run <code class="regions-code">region list allowed</code> — <code class="regions-code">bc</code> should appear in the output.</p>
              </div>
            </section>

          </div>

          <section class="regions-section">
            <h2>Need Help?</h2>
            <p>Join the <a href="https://signal.group/#CjQKIDzJC9okDwGDnwdG-531Xd-ORx1Ao6bnkqvvXMTHua1AEhDDUaR6SG6d4urCo794XsJ4" target="_blank" rel="noopener" class="regions-link">Salish Mesh</a> Signal group.</p>
          </section>

        </div>
      </div>`;

    const btns = container.querySelectorAll('.regions-role-btn');
    const panels = container.querySelectorAll('.regions-role-panel');

    btns.forEach(btn => {
      btn.addEventListener('click', () => {
        const role = btn.dataset.role;
        btns.forEach(b => b.classList.toggle('active', b === btn));
        panels.forEach(p => {
          const isActive = p.dataset.panel === role;
          p.classList.toggle('regions-role-panel--hidden', !isActive);
        });
      });
    });
  }

  function destroy() {}

  registerPage('regions', { init, destroy });
}());
